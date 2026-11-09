package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/jsintel"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/toolbuild"
	actool "github.com/Bluestar-coder/ARTEX-RedTeam/norma/tool"
)

// extractJS is the M2 trigger: statically analyze alive services' front-end JS and
// persist interfaces/sensitive hints as exploration facts. It never executes JS.
func (t *ToolSet) extractJS() actool.CoreTool {
	return toolbuild.Write(
		"extract_js",
		"对已存活的 HTTP service 资产做【纯静态】前端 JS 分析：抓取页面与 JS 文件，提取接口（路径/方法/参数/出处）、敏感信息（硬编码密钥、内网地址、测试域名、第三方 SDK、上传/支付回调/管理入口）与疑似混淆标记，写入探索图 fact。\n"+
			"不执行 JS、不做反混淆；越界 host 不抓取；单文件/单 host 有体积上限。重复执行只合并来源、不新增重复 fact。\n"+
			"返回本次扫描与落库统计；提取结果可在 graph_overview 的 js_intel 摘要与 node_detail(fact_id) 查看。",
		obj(map[string]any{
			"domain": str("目标域（范围约束：默认只允许该域及其子域；只会扫描范围内的存活 service 资产）"),
			"exclude": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "可选排除列表（域名/IP），命中的主机不会抓取"},
			"limit":     intp("最多扫描多少个存活 HTTP service（默认 30，上限 200）"),
			"intent_id": idp("可选：产生这些接口观察的意图 id；提供后 fact 会挂到该意图下"),
		}, "domain"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil || t.ts == nil {
				return actool.Errorf("extract_js 未启用: AssetStore/ExplorationStore 未初始化"), nil
			}
			var a struct {
				Domain   string   `json:"domain"`
				Exclude  []string `json:"exclude"`
				Limit    int      `json:"limit"`
				IntentID int64    `json:"intent_id"`
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
			limit := a.Limit
			if limit <= 0 {
				limit = 30
			}
			services, err := t.as.ListHTTPServicesForJS(limit)
			if err != nil {
				return actool.Errorf("查询存活服务失败: " + err.Error()), nil
			}
			targets := make([]jsintel.Target, 0, len(services))
			anchorsByHost := map[string][]int64{}
			var skippedScope []map[string]any
			for _, svc := range services {
				if svc.URL == "" {
					continue
				}
				host := recon.HostOf(svc.URL)
				if host == "" {
					continue
				}
				if ok, reason := scope.Allow(host); !ok {
					skippedScope = append(skippedScope, map[string]any{"url": svc.URL, "reason": reason})
					continue
				}
				targets = append(targets, jsintel.Target{Host: host, PageURL: svc.URL, AssetID: svc.ID})
				anchorsByHost[host] = append(anchorsByHost[host], svc.ID)
			}
			if len(targets) == 0 {
				return jsonResult(map[string]any{
					"targets": 0, "interfaces": 0, "sensitives": 0, "facts_created": 0,
					"skipped_scope": skippedScope,
					"note":          "范围内没有已存活的 HTTP service 资产；请先执行 run_recon",
				})
			}
			jsCfg := jsintel.ConfigFromEnv()
			fetcher := jsintel.NewFetcher(scope, jsCfg.Concurrency, jsCfg.Timeout)
			fetcher.MaxFileBytes = jsCfg.MaxFileBytes
			fetcher.MaxHostBytes = jsCfg.MaxHostBytes
			agg := fetcher.Scan(ctx, targets)
			now := time.Now().UTC().Format(time.RFC3339)
			created, updated := 0, 0
			upsert := func(key string, payload map[string]any, anchors []int64) error {
				id, isNew, err := t.ts.UpsertExtractedFact(key, payload, anchors)
				if err != nil {
					return err
				}
				if isNew {
					created++
				} else {
					updated++
				}
				if a.IntentID > 0 {
					_ = t.ts.Link(a.IntentID, db.RelYields, id)
				}
				return nil
			}
			for _, itf := range agg.Interfaces {
				payload := map[string]any{
					"summary":      fmt.Sprintf("接口: %s %s @%s", itf.Method, itf.Path, itf.Host),
					"detail":       fmt.Sprintf(" params=%v tags=%v rule=%s sources=%d", itf.Params, itf.Tags, itf.Rule, len(itf.Sources)),
					"evidence":     firstSnippet(itf.Sources),
					"confidence":   "observed",
					"extract_kind": "js_interface",
					"host":         itf.Host, "method": itf.Method, "path": itf.Path,
					"params": itf.Params, "tags": itf.Tags, "rule": itf.Rule,
					"extract_confidence": string(itf.Confidence),
					"sources":            itf.Sources, "source_count": len(itf.Sources), "observed_at": now,
				}
				if err := upsert("jsif|"+itf.Host+"|"+itf.Method+"|"+strings.ToLower(itf.Path), payload, anchorsByHost[itf.Host]); err != nil {
					return actool.Errorf("写入接口 fact 失败: " + err.Error()), nil
				}
			}
			for _, s := range agg.Sensitives {
				payload := map[string]any{
					"summary":      fmt.Sprintf("敏感信息: %s @%s", s.Kind, s.Host),
					"detail":       fmt.Sprintf(" rule=%s value=%s sources=%d", s.Rule, s.Value, len(s.Sources)),
					"evidence":     firstSnippet(s.Sources),
					"confidence":   "observed",
					"extract_kind": "js_sensitive",
					"host":         s.Host, "kind": s.Kind, "rule": s.Rule, "value": s.Value,
					"sources": s.Sources, "source_count": len(s.Sources), "observed_at": now,
				}
				if err := upsert("jssens|"+s.Host+"|"+s.Kind+"|"+s.Rule+"|"+s.Value, payload, anchorsByHost[s.Host]); err != nil {
					return actool.Errorf("写入敏感信息 fact 失败: " + err.Error()), nil
				}
			}
			for _, o := range agg.Obfuscations {
				payload := map[string]any{
					"summary":      fmt.Sprintf("疑似混淆: %s @%s（需人工）", o.Reason, o.Host),
					"detail":       " static heuristics only; no deobfuscation attempted. script=" + o.ScriptURL,
					"evidence":     o.Source.Snippet,
					"confidence":   "inferred",
					"extract_kind": "js_obfuscated",
					"host":         o.Host, "reason": o.Reason, "script_url": o.ScriptURL,
					"sources": []jsintel.SourceRef{o.Source}, "source_count": 1, "observed_at": now,
				}
				if err := upsert("jsobf|"+o.ScriptURL+"|"+o.Reason, payload, anchorsByHost[o.Host]); err != nil {
					return actool.Errorf("写入混淆标记失败: " + err.Error()), nil
				}
			}
			return jsonResult(map[string]any{
				"targets": len(targets), "pages": agg.Pages, "js_files": agg.JSFiles, "bytes": agg.Bytes,
				"interfaces": len(agg.Interfaces), "sensitives": len(agg.Sensitives), "obfuscated": len(agg.Obfuscations),
				"facts_created": created, "facts_updated": updated, "skipped": agg.Skipped,
				"skipped_scope": skippedScope,
			})
		},
	)
}

func firstSnippet(refs []jsintel.SourceRef) string {
	if len(refs) == 0 {
		return ""
	}
	return refs[0].Snippet
}

// jsIntelOverview folds M2 facts into graph_overview: totals, per-host
// distribution, sensitive/obfuscated counts and a few high-value entries the
// planner can turn into intents.
func (t *ToolSet) jsIntelOverview(facts []*db.Node) map[string]any {
	type ifaceFact struct {
		ID         int64
		Host       string
		Method     string
		Path       string
		Params     []any
		Tags       []any
		Sources    int
		Confidence string
	}
	var ifaces []ifaceFact
	byHost := map[string]int{}
	sensitiveTotal, obfuscatedTotal := 0, 0
	for _, n := range facts {
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		kind, _ := p["extract_kind"].(string)
		switch kind {
		case "js_interface":
			host, _ := p["host"].(string)
			byHost[host]++
			f := ifaceFact{ID: n.ID, Host: host}
			f.Method, _ = p["method"].(string)
			f.Path, _ = p["path"].(string)
			f.Confidence, _ = p["extract_confidence"].(string)
			f.Params, _ = p["params"].([]any)
			f.Tags, _ = p["tags"].([]any)
			switch v := p["source_count"].(type) {
			case float64:
				f.Sources = int(v)
			}
			ifaces = append(ifaces, f)
		case "js_sensitive":
			sensitiveTotal++
		case "js_obfuscated":
			obfuscatedTotal++
		}
	}
	if len(ifaces) == 0 && sensitiveTotal == 0 && obfuscatedTotal == 0 {
		return nil
	}
	confRank := func(c string) int {
		switch c {
		case "high":
			return 3
		case "medium":
			return 2
		default:
			return 1
		}
	}
	sort.Slice(ifaces, func(i, j int) bool {
		ri, rj := confRank(ifaces[i].Confidence), confRank(ifaces[j].Confidence)
		if ri != rj {
			return ri > rj // top_interfaces prefers high confidence
		}
		si := len(ifaces[i].Params)*3 + len(ifaces[i].Tags)*2 + ifaces[i].Sources
		sj := len(ifaces[j].Params)*3 + len(ifaces[j].Tags)*2 + ifaces[j].Sources
		if si != sj {
			return si > sj
		}
		return ifaces[i].Path < ifaces[j].Path
	})
	top := make([]map[string]any, 0, 5)
	for _, f := range ifaces {
		if len(top) >= 5 {
			break
		}
		top = append(top, map[string]any{
			"fact_id": f.ID, "host": f.Host, "method": f.Method, "path": f.Path,
			"params": f.Params, "tags": f.Tags, "source_count": f.Sources,
			"confidence": f.Confidence,
			"note":       "node_detail(fact_id) 查看原始片段与出处",
		})
	}
	// Sensitive/obfuscated top entries are useful for prioritisation; keep ids only.
	var sensitiveTop []map[string]any
	for _, n := range facts {
		if len(sensitiveTop) >= 5 {
			break
		}
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil || p["extract_kind"] != "js_sensitive" {
			continue
		}
		sensitiveTop = append(sensitiveTop, map[string]any{
			"fact_id": n.ID, "kind": p["kind"], "rule": p["rule"], "host": p["host"], "value": p["value"],
		})
	}
	byConf := map[string]int{}
	for _, f := range ifaces {
		c := f.Confidence
		if c == "" {
			c = "low"
		}
		byConf[c]++
	}
	out := map[string]any{
		"total_interfaces":   len(ifaces),
		"by_confidence":      byConf,
		"interfaces_by_host": byHost,
		"sensitive_total":    sensitiveTotal,
		"obfuscated_total":   obfuscatedTotal,
		"top_interfaces":     top,
		"note":               "接口/敏感信息来自静态 JS 分析（不执行 JS）；含管理/上传/支付 tag 的接口与 sensitive_top 优先人工核对。",
	}
	if len(sensitiveTop) > 0 {
		out["sensitive_top"] = sensitiveTop
	}
	return out
}
