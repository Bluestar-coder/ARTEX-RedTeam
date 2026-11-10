package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/jsintel"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/toolbuild"
	actool "github.com/Bluestar-coder/ARTEX-RedTeam/norma/tool"
)

// extractJSDynamic is the M3 opt-in trigger: load one page in a headless
// Chromium, observe real XHR/fetch traffic, and persist interfaces as facts.
// It is deliberately separate from extract_js so a planner cannot accidentally
// start a high-cost browser session.
func (t *ToolSet) extractJSDynamic() actool.CoreTool {
	return toolbuild.Write(
		"extract_js_dynamic",
		"【高成本、默认关闭】用无头浏览器加载一个页面并截获真实发生的 XHR/fetch 请求（URL/方法/请求体参数名/自定义头名/状态码/content-type），把接口写入探索图 fact。\n"+
			"只加载页面、等它自己发请求；不点击/不填表/不登录、不执行可疑跳转。只允许种子域及子域，外域请求在本地代理层直接拦截（不会发出）。\n"+
			"浏览器必须走项目出口代理（RECON_DYNAMIC_PROXY 或 HTTP(S)_PROXY）；未开启 RECON_DYNAMIC_ENABLED 或代理不可用时直接失败，不会直连。\n"+
			"动态结果与静态 extract_js 按 host|method|path 合并，来源标记为 dynamic（置信度 high）。",
		obj(map[string]any{
			"domain":    str("目标域（Scope 根：只允许该域及其子域；页面 host 必须在范围内）"),
			"url":       str("可选：要加载的页面 URL，默认 https://<domain>/；只加载这一个页面"),
			"intent_id": idp("可选：产生这些接口观察的意图 id；提供后 fact 会挂到该意图下"),
		}, "domain"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("extract_js_dynamic 未启用: ExplorationStore 未初始化"), nil
			}
			var a struct {
				Domain   string `json:"domain"`
				URL      string `json:"url"`
				IntentID int64  `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			domain := strings.TrimSpace(a.Domain)
			if domain == "" {
				return actool.Errorf("domain 必填"), nil
			}
			cfg := recon.DynamicConfigFromEnv()
			if !cfg.Enabled {
				return actool.Errorf("extract_js_dynamic 默认关闭：设置 RECON_DYNAMIC_ENABLED=1（并配置 RECON_DYNAMIC_PROXY 或 HTTP(S)_PROXY）后重试"), nil
			}
			scope, err := recon.NewScope(domain, nil, nil)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			pageURL := strings.TrimSpace(a.URL)
			if pageURL == "" {
				pageURL = "https://" + domain + "/"
			}
			known := t.dynamicKnownPages(domain)
			provider := recon.NewDynamicProvider(cfg)
			cap, capErr := provider.CapturePages(ctx, pageURL, scope, known)
			if capErr != nil && len(cap.Records) == 0 {
				return jsonResult(map[string]any{
					"page": pageURL, "error": capErr.Error(), "blocked": cap.Blocked,
					"observed": cap.Observed, "interfaces": 0, "duration_ms": cap.Duration.Milliseconds(),
					"proxy": cap.Proxy, "browser": cap.BrowserPath,
					"note": "动态采集失败；未写入任何接口 fact。检查 RECON_DYNAMIC_ENABLED/代理/Chrome 安装。",
				})
			}
			now := time.Now().UTC().Format(time.RFC3339)
			created, updated := 0, 0
			for _, rec := range cap.Records {
				extra := rec.Extra
				method := extraString(extra, "method")
				host := extraString(extra, "host")
				path := extraString(extra, "path")
				if method == "" || host == "" || path == "" {
					continue
				}
				params := extraStrings(extra, "params")
				queryParams := extraStrings(extra, "query_params")
				headerNames := extraStrings(extra, "header_names")
				pageURLs := extraStrings(extra, "page_urls")
				if len(pageURLs) == 0 {
					pageURLs = []string{cap.PageURL}
				}
				srcs := make([]jsintel.SourceRef, 0, len(pageURLs))
				for _, pu := range pageURLs {
					srcs = append(srcs, jsintel.SourceRef{
						PageURL: pu, ScriptURL: pu, Source: "dynamic",
						Snippet: fmt.Sprintf("dynamic request: %s %s", method, rec.Value),
					})
				}
				firstPage := pageURLs[0]
				payload := map[string]any{
					"summary":            fmt.Sprintf("动态接口: %s %s @%s", method, path, host),
					"detail":             fmt.Sprintf(" source=dynamic resource_type=%s params=%v headers=%v status=%v content_type=%v", extraString(extra, "resource_type"), params, headerNames, extra["status"], extraString(extra, "content_type")),
					"evidence":           srcs[0].Snippet,
					"confidence":         "observed",
					"source":             "dynamic",
					"extract_kind":       "js_interface",
					"extract_confidence": "high",
					"dynamic_observed":   true,
					"host":               host, "hosts": []string{host}, "method": method, "path": path,
					"params": params, "query_params": queryParams, "header_names": headerNames,
					"status": extra["status"], "content_type": extraString(extra, "content_type"),
					"resource_type": extraString(extra, "resource_type"), "page_url": firstPage,
					"page_urls": pageURLs,
					"sources":   srcs, "source_count": len(srcs), "observed_at": now,
				}
				key := "jsif|" + host + "|" + method + "|" + strings.ToLower(path)
				id, isNew, err := t.ts.UpsertExtractedFact(key, payload, nil)
				if err != nil {
					return actool.Errorf("写入动态接口 fact 失败: " + err.Error()), nil
				}
				if isNew {
					created++
				} else {
					updated++
				}
				if a.IntentID > 0 {
					_ = t.ts.Link(a.IntentID, db.RelYields, id)
				}
			}
			result := map[string]any{
				"page": cap.PageURL, "pages": cap.PagesVisited, "pages_count": cap.Pages,
				"page_errors": cap.PageErrors, "known_pages": len(known),
				"browser": cap.BrowserPath, "proxy": cap.Proxy,
				"observed_requests": cap.Observed, "interfaces": len(cap.Records),
				"blocked": cap.Blocked, "duration_ms": cap.Duration.Milliseconds(),
				"facts_created": created, "facts_updated": updated,
			}
			if capErr != nil {
				result["warning"] = capErr.Error()
			}
			return jsonResult(result)
		},
	)
}

func extraString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func extraStrings(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// dynamicKnownPages collects same-host page-like paths from endpoint assets and
// static interface facts. XHR/API-looking paths are excluded; keyword ranking is
// done by the provider when it orders candidates.
func (t *ToolSet) dynamicKnownPages(domain string) []string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Hostname() == "" {
			return
		}
		if strings.ToLower(u.Hostname()) != domain || (u.Scheme != "http" && u.Scheme != "https") {
			return
		}
		if isPageLikePath(u.Path) {
			u.RawQuery = ""
			u.Fragment = ""
			s := u.String()
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	if t.as != nil {
		if assets, err := t.as.QueryByType("endpoint", 300, 0); err == nil {
			for _, a := range assets {
				if a.Method != "" && !strings.EqualFold(a.Method, "GET") && !strings.EqualFold(a.Method, "HEAD") {
					continue
				}
				add(a.URL)
			}
		}
	}
	if t.ts != nil {
		if nodes, err := t.ts.ListByKind("fact", 2000); err == nil {
			for _, n := range nodes {
				var p map[string]any
				if json.Unmarshal(n.Payload, &p) != nil || p["extract_kind"] != "js_interface" {
					continue
				}
				host, _ := p["host"].(string)
				if !strings.EqualFold(host, domain) {
					continue
				}
				if m, _ := p["method"].(string); m != "" && !strings.EqualFold(m, "GET") && !strings.EqualFold(m, "HEAD") {
					continue
				}
				path, _ := p["path"].(string)
				if path == "" || !strings.HasPrefix(path, "/") {
					continue
				}
				add("https://" + host + path)
			}
		}
	}
	return out
}

func isPageLikePath(p string) bool {
	lp := strings.ToLower(p)
	if lp == "" || lp == "/" {
		return false
	}
	for _, frag := range []string{"/api/", "/v1/", "/v2/", "/graphql", "/rest/", "/rpc/"} {
		if strings.Contains(lp, frag) {
			return false
		}
	}
	for _, suf := range []string{".json", ".xml", ".php", ".do", ".action", ".css", ".js", ".png", ".jpg", ".svg", ".woff", ".woff2", ".pdf", ".zip"} {
		if strings.HasSuffix(lp, suf) {
			return false
		}
	}
	return true
}
