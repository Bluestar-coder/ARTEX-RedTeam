package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/toolbuild"
	actool "github.com/Bluestar-coder/ARTEX-RedTeam/norma/tool"
)

// runRecon is the M1 recon trigger: seed domain -> subdomains + alive/fingerprint
// -> asset graph. Providers are configured via ARTEX_REDTEAM_RECON_* env vars
// (external FOFA source is OFF by default); this tool intentionally has no
// provider-specific knobs.
func (t *ToolSet) runRecon() actool.CoreTool {
	return toolbuild.Write(
		"run_recon",
		"对一个种子域名执行 M1 侦察：枚举子域（证书透明度+内置字典）、探测存活/标题/favicon/技术栈，并把结果写入资产库（source/observed_at 可追踪）。\n"+
			"外部情报源默认关闭；由服务端 ARTEX_REDTEAM_RECON_* 配置控制。所有采集前都会做范围判定，域外目标直接丢弃并记日志。\n"+
			"返回本次各 provider 的统计与落库数量；重复执行只更新 last_seen，不会新增重复资产。",
		obj(map[string]any{
			"domain": str("种子域名，例如 example.com。允许范围默认是该域及其子域。"),
			"exclude": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "可选排除列表（域名或 IP），命中的目标不会发起任何请求",
			},
		}, "domain"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("run_recon 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				Domain  string   `json:"domain"`
				Exclude []string `json:"exclude"`
			}
			_ = json.Unmarshal(in, &a)
			domain := strings.TrimSpace(a.Domain)
			if domain == "" {
				return actool.Errorf("domain 必填"), nil
			}
			scope, err := recon.NewScope(domain, nil, a.Exclude)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			cfg := recon.ConfigFromEnv()
			registry := recon.DefaultRegistry(cfg, nil)
			if len(registry.Providers()) == 0 {
				return actool.Errorf("没有启用的 recon provider（检查 ARTEX_REDTEAM_RECON_ENABLED）"), nil
			}
			res := recon.Run(ctx, registry, domain, scope, cfg)
			written, err := t.as.UpsertReconAssets(t.taskID, toDBReconAssets(res.Records))
			if err != nil {
				return actool.Errorf("recon 落库失败: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"seed":      res.Seed,
				"records":   len(res.Records),
				"written":   written,
				"providers": res.Providers,
				"rejected":  res.Rejected,
			})
		},
	)
}

// toDBReconAssets converts provider records into the DB batch entry shape.
func toDBReconAssets(records []recon.Record) []db.ReconAsset {
	out := make([]db.ReconAsset, 0, len(records))
	for _, r := range records {
		out = append(out, db.ReconAsset{
			Type: string(r.Type), Value: r.Value, Parent: r.Parent,
			Source: r.Source, ObservedAt: r.ObservedAt, RawRef: r.RawRef,
			IP: r.IP, StatusCode: r.StatusCode, ContentLength: r.ContentLength,
			Title: r.Title, FaviconMMH3: r.FaviconMMH3, Technologies: r.Technologies,
		})
	}
	return out
}
