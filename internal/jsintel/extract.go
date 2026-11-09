package jsintel

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var (
	reFetch     = regexp.MustCompile("(?s)fetch\\s*\\(\\s*[`'\"]([^`'\"\\s)]+)")
	reAxios     = regexp.MustCompile("(?s)axios\\s*\\.\\s*(get|post|put|delete|patch|head|options)\\s*\\(\\s*[`'\"]([^`'\"\\s)]+)")
	reAxiosObj  = regexp.MustCompile("(?s)axios\\s*\\(\\s*\\{[^}]{0,500}?url\\s*:\\s*[`'\"]([^`'\"\\s)]+)")
	reXHR       = regexp.MustCompile("(?s)\\.open\\s*\\(\\s*[`'\"]\\s*(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\\s*[`'\"]\\s*,\\s*[`'\"]([^`'\"\\s)]+)")
	reJQAjax    = regexp.MustCompile("(?s)\\$\\.(ajax|get|post|getJSON)\\s*\\(\\s*[`'\"]([^`'\"\\s)]+)")
	reJQAjaxObj = regexp.MustCompile("(?s)\\$\\.ajax\\s*\\(\\s*\\{[^}]{0,500}?url\\s*:\\s*[`'\"]([^`'\"\\s)]+)")
	rePathLit   = regexp.MustCompile("[`'\"](/(?:api|rest|v[0-9]+|graphql|openapi)[^`'\"\\s]*)")
	reMethodOpt = regexp.MustCompile("(?i)method\\s*:\\s*[`'\"]?(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)")
	reAjaxType  = regexp.MustCompile("(?i)(?:type|method)\\s*:\\s*[`'\"]?(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)")
	reObjKey    = regexp.MustCompile("(?m)(?:^|[{,\\s])[\"']?([A-Za-z_$][A-Za-z0-9_$]*)[\"']?\\s*:")
	reTemplate  = regexp.MustCompile("\\$\\{([A-Za-z_$][A-Za-z0-9_$]*)")

	reSecret  = regexp.MustCompile("(?i)(app_?id|appid|secret_?key|secret|api_?key|apikey|access_?key|accesskey|access_?token|auth_?token|token|password|passwd|pwd)\\s*[:=]\\s*[\"'`]([^\"'`\\s]{6,160})[\"'`]")
	reIntAddr = regexp.MustCompile("(?i)(?:https?://|[`'\"])(" +
		"10\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}(?::\\d+)?|" +
		"172\\.(?:1[6-9]|2\\d|3[01])\\.\\d{1,3}\\.\\d{1,3}(?::\\d+)?|" +
		"192\\.168\\.\\d{1,3}\\.\\d{1,3}(?::\\d+)?|" +
		"localhost(?::\\d+)?)")
	reTestEnv    = regexp.MustCompile("(?i)(https?://(?:[a-z0-9-]+\\.)*(?:test|dev|staging|uat|preprod|sandbox)[.-][a-z0-9.-]*\\.[a-z]{2,})")
	reUpload     = regexp.MustCompile("(?i)[`'\"](/[^`'\"\\s]*(?:upload|/file)[^`'\"\\s]*)")
	rePayment    = regexp.MustCompile("(?i)(notify_?url|callback_?url|return_?url)\\s*[:=]\\s*[\"'`](https?://[^\"'`\\s]+)[\"'`]")
	rePaymentURL = regexp.MustCompile("(?i)https?://[^\"'`\\s]*(?:notify|callback)[^\"'`\\s]*")
	reAdminPath  = regexp.MustCompile("(?i)[`'\"](/+(?:admin|manage|manager|backend|console|system|dashboard)(?:/[^`'\"\\s]*)?)[`'\"]")
	reHexIdent   = regexp.MustCompile(`_0x[0-9a-fA-F]{4,}`)
	reEval       = regexp.MustCompile(`(?:\beval\s*\(|new\s+Function\s*\()`)
	reAtob       = regexp.MustCompile(`atob\s*\(\s*["'][A-Za-z0-9+/=]{64,}["']`)
	rePathCat    = regexp.MustCompile("[`'\"](?:/api/|/v[0-9]+/)[^`'\"\\s]*[`'\"]\\s*\\+")

	reFetchCall    = regexp.MustCompile(`(?s)\bfetch\s*\(`)
	reAxiosCall    = regexp.MustCompile(`(?s)axios\s*\.\s*(get|post|put|delete|patch|head|options)\s*\(`)
	reInstanceCall = regexp.MustCompile(`(?s)\b([A-Za-z_$][\w$]*)\s*\.\s*(get|post|put|delete|patch|head|options)\s*\(`)
	reJQCall       = regexp.MustCompile(`(?s)\$\.(get|post|getJSON)\s*\(`)
	reConfigURL    = regexp.MustCompile(`(?i)\burl\s*:\s*`)
	reBareSuffix   = regexp.MustCompile("[\"']([^\"'\\s]{2,200}\\.(?:json|php|do|action))[\"']")
)

type interfaceCandidate struct {
	host, method, path, rule string
	params, tags             []string
	confidence               Confidence
	ref                      SourceRef
}

// Extract statically analyzes one JS resource (inline script or .js file).
func Extract(host, pageURL, scriptURL, text string) Result {
	var res Result
	if host == "" || text == "" {
		return res
	}
	seenIface := map[string]*Interface{}
	addIface := func(c interfaceCandidate) {
		if c.confidence == "" {
			c.confidence = ConfidenceLow
		}
		key := c.host + "|" + c.method + "|" + strings.ToLower(c.path)
		if cur, ok := seenIface[key]; ok {
			cur.Params = uniq(append(cur.Params, c.params...))
			cur.Tags = uniq(append(cur.Tags, c.tags...))
			cur.Sources = appendSource(cur.Sources, c.ref)
			if confidenceRank(c.confidence) > confidenceRank(cur.Confidence) {
				cur.Confidence = c.confidence
				cur.Rule = c.rule
			}
			return
		}
		it := &Interface{Host: c.host, Method: c.method, Path: c.path, Params: uniq(c.params), Tags: uniq(c.tags), Rule: c.rule, Confidence: c.confidence, Sources: []SourceRef{c.ref}}
		seenIface[key] = it
	}
	manualCount := 0
	addManual := func(idx int, reason string) {
		if manualCount >= 10 {
			return
		}
		manualCount++
		res.Obfuscations = append(res.Obfuscations, Obfuscation{Host: host, Reason: "needs_manual",
			ScriptURL: scriptURL, Source: sourceAt(pageURL, scriptURL, text, idx)})
	}

	for _, m := range reFetch.FindAllStringSubmatchIndex(text, -1) {
		raw := text[m[2]:m[3]]
		start, end := m[0], m[1]
		mi, tail := inferMethod(text, end, 260)
		_ = mi
		c := buildIface(host, pageURL, scriptURL, text, raw, start, "fetch", tail)
		c.method = mi
		c.confidence = confidenceForLiteral(raw, mi)
		addIface(c)
	}
	for _, m := range reAxios.FindAllStringSubmatchIndex(text, -1) {
		raw := text[m[4]:m[5]]
		method := strings.ToUpper(text[m[2]:m[3]])
		c := buildIface(host, pageURL, scriptURL, text, raw, m[0], "axios", text[m[1]:minInt(len(text), m[1]+200)])
		c.method = method
		c.confidence = ConfidenceHigh
		addIface(c)
	}
	for _, m := range reAxiosObj.FindAllStringSubmatchIndex(text, -1) {
		raw := text[m[2]:m[3]]
		method := methodFromWindow(text[m[1]:minInt(len(text), m[1]+400)])
		c := buildIface(host, pageURL, scriptURL, text, raw, m[0], "axios_object", text[m[1]:minInt(len(text), m[1]+400)])
		if method != "unknown" {
			c.method = method
			c.confidence = ConfidenceHigh
		} else {
			c.confidence = ConfidenceLow
		}
		addIface(c)
	}
	for _, m := range reXHR.FindAllStringSubmatchIndex(text, -1) {
		raw := text[m[4]:m[5]]
		method := strings.ToUpper(text[m[2]:m[3]])
		c := buildIface(host, pageURL, scriptURL, text, raw, m[0], "xhr", text[m[1]:minInt(len(text), m[1]+200)])
		c.method = method
		c.confidence = ConfidenceHigh
		addIface(c)
	}
	for _, m := range reJQAjax.FindAllStringSubmatchIndex(text, -1) {
		raw := text[m[4]:m[5]]
		call := strings.ToLower(text[m[2]:m[3]])
		method := "unknown"
		switch call {
		case "get", "getjson":
			method = "GET"
		case "post":
			method = "POST"
		default:
			method = methodFromWindow(text[m[1]:minInt(len(text), m[1]+400)])
		}
		c := buildIface(host, pageURL, scriptURL, text, raw, m[0], "jquery_"+call, text[m[1]:minInt(len(text), m[1]+400)])
		c.method = method
		if method != "unknown" {
			c.confidence = ConfidenceHigh
		} else {
			c.confidence = ConfidenceLow
		}
		addIface(c)
	}
	for _, m := range reJQAjaxObj.FindAllStringSubmatchIndex(text, -1) {
		raw := text[m[2]:m[3]]
		c := buildIface(host, pageURL, scriptURL, text, raw, m[0], "jquery_ajax_object", text[m[1]:minInt(len(text), m[1]+400)])
		if method := methodFromWindow(text[m[1]:minInt(len(text), m[1]+400)]); method != "unknown" {
			c.method = method
			c.confidence = ConfidenceHigh
		} else {
			c.confidence = ConfidenceLow
		}
		addIface(c)
	}
	// Any literal API-ish path (catch bundled route tables / constants).
	for _, m := range rePathLit.FindAllStringSubmatchIndex(text, -1) {
		if isAssignmentRHS(text, m[0]) || isBaseURLValue(text, m[0]) {
			continue // constant/baseURL definitions are not interfaces by themselves
		}
		raw := text[m[2]:m[3]]
		c := buildIface(host, pageURL, scriptURL, text, raw, m[0], "path_literal", "")
		c.confidence = ConfidenceLow
		addIface(c)
	}

	// RULE_TEMPLATE_BEGIN
	// --- M3 rules: expression-based recovery (template strings, one-level
	// constants, axios baseURL, config objects, bare suffixes). Still static.
	consts := fileConstMap(text)
	bases := map[string]string{}
	addEval := func(expr, method, rule string, start int, prefix string) (path string, conf Confidence) {
		p, _, evalConf, unresolved, used := evalPathExpr(expr, consts, bases)
		if !used || p == "" {
			if unresolved >= 1 && !used {
				addManual(start, "unresolved path expression")
			}
			return "", ""
		}
		if prefix != "" && !strings.Contains(p, "://") {
			p = joinBaseURL(prefix, p)
		}
		c := buildIface(host, pageURL, scriptURL, text, p, start, rule, "")
		if method != "" {
			c.method = method
		} else {
			c.method = "unknown"
		}
		cConf := evalConf
		switch rule {
		case "template", "const_fold", "axios_baseurl", "config_object_template", "config_object_fold":
			cConf = ConfidenceMedium
		}
		if c.method == "unknown" {
			if cConf == ConfidenceMedium {
				c.confidence = ConfidenceMedium
			} else {
				c.confidence = ConfidenceLow
			}
		} else {
			c.confidence = cConf
		}
		addIface(c)
		return p, c.confidence
	}

	// fetch(EXPR, {method}) — template / concatenation / constants.
	for _, m := range reFetchCall.FindAllStringIndex(text, -1) {
		open := m[1] - 1
		expr, end, ok := readCallArg(text, open)
		if !ok {
			continue
		}
		method := "unknown"
		if mm := reMethodOpt.FindStringSubmatch(trimCallWindow(text[end:minInt(len(text), end+220)])); len(mm) == 2 {
			method = strings.ToUpper(mm[1])
		}
		rule := "fetch"
		if strings.Contains(expr, "${") {
			rule = "template"
		} else if strings.Contains(expr, "+") {
			rule = "const_fold"
		}
		addEval(expr, method, rule, m[0], "")
	}

	seenSens := map[string]*Sensitive{}
	addSens := func(s Sensitive) {
		key := s.Host + "|" + s.Kind + "|" + s.Rule + "|" + s.Value
		if cur, ok := seenSens[key]; ok {
			cur.Sources = append(cur.Sources, s.Sources...)
			return
		}
		seenSens[key] = &s
	}
	for _, m := range reSecret.FindAllStringSubmatchIndex(text, -1) {
		key := strings.ToLower(text[m[2]:m[3]])
		value := text[m[4]:m[5]]
		if !plausibleSecret(value, key) {
			continue
		}
		addSens(Sensitive{Host: host, Kind: "hardcoded_secret", Rule: "hardcoded_secret:" + key,
			Value: maskSecret(value), Sources: []SourceRef{sourceAt(pageURL, scriptURL, text, m[0])}})
	}
	for _, m := range reIntAddr.FindAllStringSubmatchIndex(text, -1) {
		val := text[m[2]:m[3]]
		addSens(Sensitive{Host: host, Kind: "internal_address", Rule: "internal_address", Value: val,
			Sources: []SourceRef{sourceAt(pageURL, scriptURL, text, m[0])}})
	}
	for _, m := range reTestEnv.FindAllStringSubmatchIndex(text, -1) {
		val := text[m[2]:m[3]]
		addSens(Sensitive{Host: host, Kind: "test_env_domain", Rule: "test_env_domain", Value: val,
			Sources: []SourceRef{sourceAt(pageURL, scriptURL, text, m[0])}})
	}
	for _, sdk := range thirdPartyRules {
		for _, m := range sdk.re.FindAllStringSubmatchIndex(text, -1) {
			val := text[m[0]:m[1]]
			addSens(Sensitive{Host: host, Kind: "third_party_sdk", Rule: "sdk:" + sdk.name, Value: clip(val, 120),
				Sources: []SourceRef{sourceAt(pageURL, scriptURL, text, m[0])}})
		}
	}
	for _, m := range reUpload.FindAllStringSubmatchIndex(text, -1) {
		val := text[m[2]:m[3]]
		addSens(Sensitive{Host: host, Kind: "upload_hint", Rule: "upload_endpoint", Value: val,
			Sources: []SourceRef{sourceAt(pageURL, scriptURL, text, m[0])}})
	}
	for _, m := range rePayment.FindAllStringSubmatchIndex(text, -1) {
		val := text[m[4]:m[5]]
		addSens(Sensitive{Host: host, Kind: "payment_callback", Rule: "payment_callback:" + strings.ToLower(text[m[2]:m[3]]), Value: val,
			Sources: []SourceRef{sourceAt(pageURL, scriptURL, text, m[0])}})
	}
	for _, m := range rePaymentURL.FindAllStringIndex(text, -1) {
		val := text[m[0]:m[1]]
		addSens(Sensitive{Host: host, Kind: "payment_callback", Rule: "payment_callback:url", Value: val,
			Sources: []SourceRef{sourceAt(pageURL, scriptURL, text, m[0])}})
	}
	for _, m := range reAdminPath.FindAllStringSubmatchIndex(text, -1) {
		val := text[m[2]:m[3]]
		addSens(Sensitive{Host: host, Kind: "admin_path", Rule: "admin_path", Value: val,
			Sources: []SourceRef{sourceAt(pageURL, scriptURL, text, m[0])}})
	}

	for _, it := range seenIface {
		res.Interfaces = append(res.Interfaces, *it)
	}
	for _, s := range seenSens {
		res.Sensitives = append(res.Sensitives, *s)
	}
	sort.Slice(res.Interfaces, func(i, j int) bool {
		if res.Interfaces[i].Host != res.Interfaces[j].Host {
			return res.Interfaces[i].Host < res.Interfaces[j].Host
		}
		if res.Interfaces[i].Path != res.Interfaces[j].Path {
			return res.Interfaces[i].Path < res.Interfaces[j].Path
		}
		return res.Interfaces[i].Method < res.Interfaces[j].Method
	})
	sort.Slice(res.Sensitives, func(i, j int) bool {
		if res.Sensitives[i].Kind != res.Sensitives[j].Kind {
			return res.Sensitives[i].Kind < res.Sensitives[j].Kind
		}
		return res.Sensitives[i].Value < res.Sensitives[j].Value
	})
	res.Obfuscations = append(res.Obfuscations, detectObfuscation(host, pageURL, scriptURL, text)...)
	return res
}

func buildIface(host, pageURL, scriptURL, text, raw string, start int, rule, window string) interfaceCandidate {
	h, path, query, tmpl := normalizePath(host, raw)
	params := query
	params = append(params, tmpl...)
	params = append(params, objectKeys(window, 12)...)
	c := interfaceCandidate{host: h, method: "unknown", path: path, rule: rule, params: params}
	c.tags = classifyPath(path)
	c.ref = sourceAt(pageURL, scriptURL, text, start)
	return c
}

// normalizePath turns a matched string into host + pathname + params.
func normalizePath(defaultHost, raw string) (host, path string, params, templateVars []string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "javascript:") || strings.HasPrefix(raw, "#") {
		return defaultHost, "", nil, nil
	}
	host = defaultHost
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			host = strings.ToLower(u.Hostname())
			path = u.Path
			for k := range u.Query() {
				params = append(params, k)
			}
		}
	} else {
		path = raw
		if i := strings.IndexByte(path, '?'); i >= 0 {
			if u, err := url.Parse("http://h" + path[i:]); err == nil {
				for k := range u.Query() {
					params = append(params, k)
				}
			}
			path = path[:i]
		}
	}
	if path == "" {
		path = "/"
	}
	for _, m := range reTemplate.FindAllStringSubmatch(path, -1) {
		templateVars = append(templateVars, m[1])
		path = strings.ReplaceAll(path, "${"+m[1]+"}", "{"+m[1]+"}")
	}
	return host, path, params, templateVars
}

func objectKeys(window string, cap int) []string {
	if window == "" {
		return nil
	}
	if len(window) > 600 {
		window = window[:600]
	}
	skip := map[string]bool{"method": true, "body": true, "headers": true, "mode": true, "credentials": true,
		"cache": true, "redirect": true, "referrer": true, "signal": true, "type": true, "url": true,
		"success": true, "error": true, "complete": true, "dataType": true, "contentType": true, "timeout": true}
	var out []string
	for _, m := range reObjKey.FindAllStringSubmatch(window, -1) {
		if len(m) < 2 || skip[m[1]] || len(out) >= cap {
			continue
		}
		out = append(out, m[1])
	}
	return out
}

func inferMethod(text string, after int, window int) (string, string) {
	tail := trimCallWindow(text[after:minInt(len(text), after+window)])
	if m := reMethodOpt.FindStringSubmatch(tail); len(m) == 2 {
		return strings.ToUpper(m[1]), tail
	}
	return "unknown", tail
}

func methodFromWindow(window string) string {
	window = trimCallWindow(window)
	if m := reAjaxType.FindStringSubmatch(window); len(m) == 2 {
		return strings.ToUpper(m[1])
	}
	return "unknown"
}

// trimCallWindow bounds option/method inference to the current call. Without
// this, a missing method on call N can borrow the method of call N+1 further
// down the file (observed during test: first fetch inherited the next fetch's
// POST/GET).
func trimCallWindow(s string) string {
	if len(s) > 400 {
		s = s[:400]
	}
	cut := len(s)
	for _, sep := range []string{")", ";", "\n", "}"} {
		if i := strings.Index(s, sep); i >= 0 && i < cut {
			cut = i
		}
	}
	return s[:cut]
}

func classifyPath(path string) []string {
	lower := strings.ToLower(path)
	var tags []string
	if strings.Contains(lower, "/admin") || strings.Contains(lower, "/manage") || strings.Contains(lower, "/backend") || strings.Contains(lower, "/console") {
		tags = append(tags, "admin")
	}
	if strings.Contains(lower, "upload") || strings.Contains(lower, "/file") {
		tags = append(tags, "upload")
	}
	if strings.Contains(lower, "notify") || strings.Contains(lower, "callback") || strings.Contains(lower, "pay") {
		tags = append(tags, "payment")
	}
	return tags
}

func sourceAt(pageURL, scriptURL, text string, idx int) SourceRef {
	if idx < 0 {
		idx = 0
	}
	start := idx - snippetRadius
	if start < 0 {
		start = 0
	}
	end := idx + snippetRadius
	if end > len(text) {
		end = len(text)
	}
	return SourceRef{PageURL: pageURL, ScriptURL: scriptURL, Line: 1 + strings.Count(text[:idx], "\n"), Offset: idx,
		Snippet: strings.TrimSpace(text[start:end])}
}

func plausibleSecret(value, key string) bool {
	v := strings.TrimSpace(value)
	if len(v) < 8 && key != "appid" && key != "app_id" {
		return false
	}
	lower := strings.ToLower(v)
	for _, bad := range []string{"${", "process.env", "import.meta", "your_", "your-", "xxx", "****", "changeme", "placeholder", "example", "undefined", "null", "true", "false"} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	uniq := map[rune]bool{}
	for _, r := range v {
		uniq[r] = true
	}
	if len(uniq) <= 2 { // aaaaaaaa / 00000000
		return false
	}
	return true
}

func maskSecret(v string) string {
	if len(v) <= 8 {
		return "***"
	}
	return v[:4] + "…" + v[len(v)-2:]
}

func detectObfuscation(host, pageURL, scriptURL, text string) []Obfuscation {
	var out []Obfuscation
	add := func(reason string, idx int) {
		out = append(out, Obfuscation{Host: host, Reason: reason, ScriptURL: scriptURL,
			Source: sourceAt(pageURL, scriptURL, text, idx)})
	}
	if n := len(reHexIdent.FindAllString(text, -1)); n >= 5 {
		loc := reHexIdent.FindStringIndex(text)
		add("hex_identifiers", loc[0])
	}
	if loc := reEval.FindStringIndex(text); loc != nil {
		add("dynamic_eval", loc[0])
	}
	if loc := reAtob.FindStringIndex(text); loc != nil {
		add("base64_decode", loc[0])
	}
	if loc := rePathCat.FindStringIndex(text); loc != nil {
		add("path_concatenation", loc[0])
	}
	return out
}

type thirdPartyRule struct {
	name string
	re   *regexp.Regexp
}

var thirdPartyRules = []thirdPartyRule{
	{"amap", regexp.MustCompile(`(?i)(?:webapi\.amap\.com|AMap\.\w+)`)},
	{"baidu_map", regexp.MustCompile(`(?i)(?:api\.map\.baidu\.com|BMap\.\w+)`)},
	{"google_maps", regexp.MustCompile(`(?i)maps\.googleapis\.com`)},
	{"wechat_jssdk", regexp.MustCompile(`(?i)(?:res\.wx\.qq\.com|wx\.config\s*\()`)},
	{"alipay", regexp.MustCompile(`(?i)(?:alipayobjects\.com|AlipayJSBridge)`)},
	{"umeng", regexp.MustCompile(`(?i)(?:cnzz\.com|umeng\.com)`)},
	{"jpush", regexp.MustCompile(`(?i)jpush\.cn`)},
	{"bugly", regexp.MustCompile(`(?i)bugly\.qq\.com`)},
	{"sentry", regexp.MustCompile(`(?i)(?:sentry\.io|Sentry\.init\s*\()`)},
	{"stripe", regexp.MustCompile(`(?i)(?:js\.stripe\.com|Stripe\s*\()`)},
	{"firebase", regexp.MustCompile(`(?i)(?:firebaseapp\.com|firebase\.initializeApp\s*\()`)},
	{"aws_sdk", regexp.MustCompile(`(?i)aws-sdk`)},
}

func appendSource(list []SourceRef, ref SourceRef) []SourceRef {
	for _, cur := range list {
		if cur.ScriptURL == ref.ScriptURL && cur.Offset == ref.Offset && cur.PageURL == ref.PageURL {
			return list
		}
	}
	return append(list, ref)
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func confidenceForLiteral(raw, method string) Confidence {
	if method == "unknown" || method == "" {
		return ConfidenceLow
	}
	if strings.Contains(raw, "${") || strings.Contains(raw, "+") {
		return ConfidenceMedium
	}
	return ConfidenceHigh
}

func joinBaseURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "/") {
		return base + path
	}
	return base + "/" + path
}

// isAssignmentRHS reports whether the literal starts a const/let/var assignment
// value (a constant table entry is not an interface by itself).
func isAssignmentRHS(text string, start int) bool {
	lo := start - 80
	if lo < 0 {
		lo = 0
	}
	prev := text[lo:start]
	return regexp.MustCompile("(?i)(?:const|let|var)\\s+[A-Za-z_$][\\w$]*\\s*=\\s*$").MatchString(prev)
}

// isBaseURLValue reports whether the literal is the value of a baseURL option;
// the concrete path comes from the axios instance call rule instead.
func isBaseURLValue(text string, start int) bool {
	lo := start - 60
	if lo < 0 {
		lo = 0
	}
	prev := text[lo:start]
	return regexp.MustCompile("(?i)baseURL\\s*:\\s*$").MatchString(prev)
}
