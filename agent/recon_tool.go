package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
			out, err := performRecon(ctx, t.as, t.taskID, domain, a.Exclude, recon.ConfigFromEnv())
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(out)
		},
	)
}

// performRecon is the shared asset-discovery path used by the run_recon tool and
// the deterministic pre-planner phase. Dynamic browser capture is deliberately
// excluded here: run_recon is asset discovery, extract_js_dynamic owns browser
// capture. Keeping them separate prevents a recon call from silently launching a
// browser and from producing interface records that cannot be persisted as assets.
func performRecon(ctx context.Context, as *db.AssetStore, taskID int64, domain string, exclude []string, cfg recon.Config) (map[string]any, error) {
	if as == nil {
		return nil, fmt.Errorf("run_recon 未启用: AssetStore 未初始化")
	}
	if taskID > 0 {
		scopes, err := as.ListTaskScope(taskID)
		if err != nil {
			return nil, fmt.Errorf("读取任务 scope 失败: %w", err)
		}
		networkScope := false
		for _, sc := range scopes {
			switch sc.Kind {
			case "root_domain", "subdomain", "ip", "cidr":
				networkScope = true
			}
		}
		if !networkScope {
			return nil, fmt.Errorf("任务没有可用网络 scope（root_domain/subdomain/ip/cidr），拒绝执行 recon；请先在任务范围里添加目标")
		}
	}
	scope, err := recon.NewScope(domain, nil, exclude)
	if err != nil {
		return nil, err
	}
	// Dynamic is a separate phase/tool; never run it from asset discovery.
	cfg.Dynamic.Enabled = false
	registry := recon.DefaultRegistry(cfg, nil)
	if len(registry.Providers()) == 0 {
		return nil, fmt.Errorf("没有启用的 recon provider（检查 ARTEX_REDTEAM_RECON_ENABLED）")
	}
	res := recon.Run(ctx, registry, domain, scope, cfg)
	assetRecords := assetOnlyReconRecords(res.Records)
	written, err := as.UpsertReconAssets(taskID, toDBReconAssets(assetRecords))
	if err != nil {
		return nil, fmt.Errorf("recon 落库失败: %w", err)
	}
	return map[string]any{
		"seed":               res.Seed,
		"records":            len(res.Records),
		"asset_records":      len(assetRecords),
		"interfaces_skipped": len(res.Records) - len(assetRecords),
		"written":            written,
		"providers":          res.Providers,
		"rejected":           res.Rejected,
	}, nil
}

// assetOnlyReconRecords drops interface records that the dynamic provider can
// emit during run_recon. They are facts, not assets; UpsertReconAssets rejects
// unknown asset types, which would drop the whole batch.
func assetOnlyReconRecords(records []recon.Record) []recon.Record {
	out := make([]recon.Record, 0, len(records))
	for _, rec := range records {
		if rec.Type == recon.TypeInterface {
			continue
		}
		out = append(out, rec)
	}
	return out
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
