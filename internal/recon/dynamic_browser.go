package recon

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// captureBrowser is the only code that touches a real browser. Chrome points at
// a local scope-enforcing proxy (scopeProxy) which itself tunnels through the
// configured egress proxy; the browser never gets a direct route. We use the
// Network domain for observation only, not Fetch interception: Fetch pauses the
// document request and deadlocks Page.navigate on a single CDP session. Scope is
// enforced one hop earlier, at the local proxy, so blocked requests are never
// sent at all.
func captureBrowser(ctx context.Context, pageURL string, scope Scope, cfg DynamicConfig, known []string) (DynamicCapture, error) {
	out := DynamicCapture{PageURL: pageURL, Proxy: redactProxy(cfg.Proxy)}
	chrome, err := findChromePath(cfg.BrowserPath)
	if err != nil {
		return out, err
	}
	out.BrowserPath = chrome
	sp, err := newScopeProxy(cfg.Proxy, scope)
	if err != nil {
		return out, err
	}
	defer sp.Close()
	profileDir, err := os.MkdirTemp("", "artex-dynamic-*")
	if err != nil {
		return out, err
	}
	defer cleanupChromeProfile(profileDir)

	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(chrome),
		chromedp.ProxyServer(sp.URL()),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("disable-quic", true),
		chromedp.Flag("renderer-process-limit", "1"),
		chromedp.Flag("js-flags", fmt.Sprintf("--max-old-space-size=%d", cfg.MaxMemoryMB)),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	var current atomic.Pointer[captureState]
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx, chromedp.WithListener(func(ev any) {
		if st := current.Load(); st != nil {
			st.handle(ev)
		}
	}))
	defer cancelBrowser()
	if _, err := chromedp.Call(browserCtx, network.Enable, network.EnableParams{}); err != nil {
		return out, fmt.Errorf("network enable failed: %w", err)
	}

	home, err := url.Parse(pageURL)
	if err != nil || home.Hostname() == "" {
		return out, fmt.Errorf("invalid page URL %q", pageURL)
	}
	homeHost := strings.ToLower(home.Hostname())
	seen := map[string]bool{}
	merged := map[string]*Record{}
	mergePage := func(st *captureState) {
		for _, rec := range st.records() {
			key := strings.ToUpper(strExtraValue(rec.Extra, "method")) + "|" + strings.ToLower(rec.Parent) + "|" + strings.ToLower(strExtraValue(rec.Extra, "path"))
			if old, ok := merged[key]; ok {
				mergePageRecord(old, rec)
				continue
			}
			cp := rec
			merged[key] = &cp
		}
	}

	// Root first (needed to discover DOM links), then caller-provided known page
	// paths sorted by keyword priority; DOM links are appended after those.
	queue := []string{normalizePageURL(pageURL)}
	knownURLs := sanitizeCandidates(known, home, homeHost)
	sort.SliceStable(knownURLs, func(i, j int) bool { return candidateScore(knownURLs[i]) > candidateScore(knownURLs[j]) })
	queue = append(queue, knownURLs...)
	first := true
	var firstErr error
	pages := 0
	for len(queue) > 0 && pages < cfg.MaxPages && ctx.Err() == nil {
		raw := queue[0]
		queue = queue[1:]
		normalized := normalizePageURL(raw)
		if normalized == "" || seen[normalized] {
			continue
		}
		u, err := url.Parse(normalized)
		if err != nil || strings.ToLower(u.Hostname()) != homeHost {
			continue
		}
		seen[normalized] = true
		pages++
		out.PagesVisited = append(out.PagesVisited, normalized)

		st := newCaptureState(scope)
		st.pageURL = normalized
		current.Store(st)
		navCtx, cancelNav := context.WithTimeout(browserCtx, cfg.PageTimeout)
		navErr := chromedp.Do(navCtx, chromedp.Navigate(normalized))
		cancelNav()
		if navErr == nil && cfg.IdleWait > 0 {
			wait := cfg.IdleWait
			if wait > cfg.PageTimeout {
				wait = cfg.PageTimeout
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
		}
		if navErr == nil {
			// A page that navigated itself off-host is not followed or recorded.
			if final, ferr := chromedp.Run(browserCtx, chromedp.Evaluate[string](`location.href`)); ferr == nil {
				if fu, e := url.Parse(final); e == nil && strings.ToLower(fu.Hostname()) != homeHost {
					out.PageErrors = append(out.PageErrors, normalized+" -> out_of_scope_final:"+final)
					navErr = fmt.Errorf("final URL out of scope: %s", final)
				}
			}
		}
		if navErr == nil && first {
			hrefs, herr := chromedp.Run(browserCtx, chromedp.Evaluate[[]string](`Array.from(document.querySelectorAll('a[href]')).map(a=>a.href)`))
			if herr == nil {
				links := sanitizeCandidates(hrefs, home, homeHost)
				sort.SliceStable(links, func(i, j int) bool { return candidateScore(links[i]) > candidateScore(links[j]) })
				queue = append(queue, links...)
			}
		}
		first = false
		out.Observed += st.observedCount()
		if navErr == nil {
			if c := chromedp.FromContext(browserCtx); c != nil && c.Target != nil {
				st.fillPostData(c.Target, browserCtx)
			}
		}
		if navErr == nil && st.redirectCount() > 3 {
			out.PageErrors = append(out.PageErrors, normalized+" redirect_chain>3")
			navErr = fmt.Errorf("redirect chain exceeded 3")
		}
		// Keep partial captures even when the page timed out or its redirect chain
		// was cut; the already-recorded requests are real observations.
		mergePage(st)
		if navErr != nil {
			out.PageErrors = append(out.PageErrors, normalized+": "+navErr.Error())
			if firstErr == nil {
				firstErr = navErr
			}
		}
		current.Store(nil)
		// Drop page state before the next navigation to keep memory bounded.
		clearCtx, cancelClear := context.WithTimeout(browserCtx, 2*time.Second)
		_ = chromedp.Do(clearCtx, chromedp.Navigate("about:blank"))
		cancelClear()
	}
	out.Blocked = sp.blockedList()
	out.Records = make([]Record, 0, len(merged))
	for _, rec := range merged {
		out.Records = append(out.Records, *rec)
	}
	sort.Slice(out.Records, func(i, j int) bool {
		if out.Records[i].Parent != out.Records[j].Parent {
			return out.Records[i].Parent < out.Records[j].Parent
		}
		pi, _ := out.Records[i].Extra["path"].(string)
		pj, _ := out.Records[j].Extra["path"].(string)
		if pi != pj {
			return pi < pj
		}
		mi, _ := out.Records[i].Extra["method"].(string)
		mj, _ := out.Records[j].Extra["method"].(string)
		return mi < mj
	})
	out.Pages = len(out.PagesVisited)
	out.XHRCount = len(out.Records)
	if ctx.Err() != nil {
		return out, fmt.Errorf("host budget reached: %w", ctx.Err())
	}
	if pages == 0 && firstErr == nil {
		firstErr = fmt.Errorf("no pages loaded")
	}
	if len(out.Records) == 0 && firstErr != nil {
		return out, fmt.Errorf("no interfaces captured: %w", firstErr)
	}
	return out, nil
}

func strExtraValue(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mergePageRecord(old *Record, add Record) {
	oldPages, _ := old.Extra["page_urls"].([]string)
	if addPage, ok := add.Extra["page_url"].(string); ok && addPage != "" {
		oldPages = appendUnique(oldPages, addPage)
	}
	old.Extra["page_urls"] = oldPages
	if len(oldPages) > 0 {
		old.Extra["page_url"] = oldPages[0]
		old.Extra["source_count"] = len(oldPages)
	}
	for _, k := range []string{"params", "query_params", "header_names"} {
		oldV, _ := old.Extra[k].([]string)
		addV, _ := add.Extra[k].([]string)
		old.Extra[k] = appendUnique(oldV, addV...)
	}
	if st, ok := add.Extra["status"].(int64); ok && st != 0 {
		old.Extra["status"] = st
	}
	if ct, ok := add.Extra["content_type"].(string); ok && ct != "" {
		old.Extra["content_type"] = ct
	}
}

func normalizePageURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Fragment = ""
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		u.Host = host + ":" + port
	} else {
		u.Host = host
	}
	if u.Path == "" {
		u.Path = "/"
	}
	if u.Path != "/" {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	return u.String()
}

func sanitizeCandidates(raws []string, home *url.URL, homeHost string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(strings.ToLower(raw), "javascript:") || strings.HasPrefix(strings.ToLower(raw), "mailto:") || strings.HasPrefix(strings.ToLower(raw), "tel:") {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if !u.IsAbs() {
			u = home.ResolveReference(u)
		}
		if u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		if strings.ToLower(u.Hostname()) != homeHost {
			continue
		}
		if isStaticAssetPath(u.Path) || looksNonPagePath(u.Path) {
			continue
		}
		n := normalizePageURL(u.String())
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

func looksNonPagePath(p string) bool {
	lp := strings.ToLower(p)
	for _, frag := range []string{"/api/", "/v1/", "/v2/", "/graphql", "/rest/"} {
		if strings.Contains(lp, frag) {
			return true
		}
	}
	for _, suf := range []string{".json", ".xml", ".php", ".do", ".action"} {
		if strings.HasSuffix(lp, suf) {
			return true
		}
	}
	return false
}

var dynamicKeywordWeights = map[string]int{
	"login": 10, "signin": 10, "sign-in": 10, "register": 9, "account": 8, "user": 8,
	"admin": 8, "console": 8, "dashboard": 7, "pay": 7, "order": 6, "detail": 6,
	"list": 5, "api": 4,
}

func candidateScore(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	score := 0
	lp := strings.ToLower(u.Path)
	for kw, w := range dynamicKeywordWeights {
		if strings.Contains(lp, kw) {
			score += w
		}
	}
	if u.Path == "/" || u.Path == "" {
		score += 3
	}
	score -= len(u.Path) / 40
	return score
}

type capturedRequest struct {
	url            string
	method         string
	resourceType   string
	queryParams    []string
	bodyParams     []string
	headerNames    []string
	status         int64
	contentType    string
	hasPostData    bool
	requestID      network.RequestID
	postDataInline string
}

type captureState struct {
	mu        sync.Mutex
	scope     Scope
	pageURL   string
	requests  map[string]*capturedRequest
	order     []string
	observed  int
	loaded    bool
	redirects int
}

func newCaptureState(scope Scope) *captureState {
	return &captureState{scope: scope, requests: map[string]*capturedRequest{}}
}

func (s *captureState) handle(ev any) {
	switch e := ev.(type) {
	case *network.EventRequestWillBeSent:
		if e.Request == nil || e.Request.URL == "" {
			return
		}
		s.upsert(e, e.Request)
	case *network.EventResponseReceived:
		if e.Response == nil {
			return
		}
		s.mu.Lock()
		if r, ok := s.requests[string(e.RequestID)]; ok {
			r.status = e.Response.Status
			r.contentType = e.Response.MimeType
		}
		s.mu.Unlock()
	}
}

func (s *captureState) upsert(e *network.EventRequestWillBeSent, req *network.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := string(e.RequestID)
	r, ok := s.requests[id]
	if !ok {
		r = &capturedRequest{url: req.URL}
		s.requests[id] = r
		s.order = append(s.order, id)
		s.observed++
	}
	r.url = req.URL
	r.method = strings.ToUpper(req.Method)
	r.resourceType = string(e.Type)
	r.requestID = e.RequestID
	r.hasPostData = req.HasPostData
	r.headerNames = customHeaderNames(req.Headers)
	if u, err := url.Parse(req.URL); err == nil && u.RawQuery != "" {
		if vals, err := url.ParseQuery(u.RawQuery); err == nil {
			for k := range vals {
				r.queryParams = appendUnique(r.queryParams, k)
			}
		}
	}
	for _, ent := range req.PostDataEntries {
		if len(ent.Bytes) > 0 {
			r.postDataInline += string(ent.Bytes)
		}
	}
	if r.postDataInline != "" {
		r.bodyParams = paramNames(r.postDataInline)
	}
}

// fillPostData runs after page load (no navigation command pending) and asks the
// network domain for body bytes it did not inline. Only names survive.
func (s *captureState) fillPostData(target *chromedp.Target, ctx context.Context) {
	s.mu.Lock()
	var ids []network.RequestID
	for _, r := range s.requests {
		if r.hasPostData && len(r.bodyParams) == 0 && isInterfaceResource(r.resourceType) {
			ids = append(ids, r.requestID)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		var res network.GetRequestPostDataResult
		if err := target.Call(ctx, "Network.getRequestPostData", &network.GetRequestPostDataParams{RequestID: id}, &res); err != nil {
			continue
		}
		s.mu.Lock()
		if r, ok := s.requests[string(id)]; ok {
			r.bodyParams = paramNames(string(res.PostData))
		}
		s.mu.Unlock()
	}
}

func (s *captureState) redirectCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.redirects
}

func (s *captureState) observedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observed
}

func (s *captureState) records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]Record{}
	for _, id := range s.order {
		r := s.requests[id]
		if r == nil || !isInterfaceResource(r.resourceType) {
			continue
		}
		u, err := url.Parse(r.url)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		// Network events may still report an attempted out-of-scope request even
		// though the scope proxy failed it before dialing. It must never become a
		// result; blocked URLs are logged separately.
		if ok, _ := s.scope.Allow(u.Hostname()); !ok {
			continue
		}
		if isStaticAssetPath(u.Path) {
			continue
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		if strings.EqualFold(r.method, "GET") && (strings.HasPrefix(strings.ToLower(r.contentType), "text/html") || path == "/") {
			continue // page navigations are not interfaces
		}
		key := strings.ToUpper(r.method) + "|" + strings.ToLower(u.Hostname()) + "|" + path
		if old, ok := seen[key]; ok {
			mergeDynamicRecord(&old, r)
			seen[key] = old
			continue
		}
		rec := Record{
			Type: TypeInterface, Value: u.Scheme + "://" + u.Hostname() + path, Parent: u.Hostname(),
			Source: ProviderDynamic, ObservedAt: time.Now().UTC(), RawRef: r.url,
			Extra: map[string]any{
				"method": strings.ToUpper(r.method), "host": u.Hostname(), "path": path,
				"params": r.bodyParams, "query_params": r.queryParams, "header_names": r.headerNames,
				"status": r.status, "content_type": r.contentType, "resource_type": r.resourceType,
				"page_url": s.pageURL, "page_urls": []string{s.pageURL}, "source_count": 1, "source": "dynamic",
			},
		}
		seen[key] = rec
	}
	out := make([]Record, 0, len(seen))
	for _, rec := range seen {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Parent != out[j].Parent {
			return out[i].Parent < out[j].Parent
		}
		pi, _ := out[i].Extra["path"].(string)
		pj, _ := out[j].Extra["path"].(string)
		if pi != pj {
			return pi < pj
		}
		mi, _ := out[i].Extra["method"].(string)
		mj, _ := out[j].Extra["method"].(string)
		return mi < mj
	})
	return out
}

func mergeDynamicRecord(old *Record, r *capturedRequest) {
	for _, k := range []string{"params", "query_params", "header_names"} {
		if v, ok := old.Extra[k].([]string); ok {
			old.Extra[k] = appendUnique(v, parameterListFor(k, r)...)
		}
	}
	if r.status != 0 {
		old.Extra["status"] = r.status
	}
	if r.contentType != "" {
		old.Extra["content_type"] = r.contentType
	}
}

func parameterListFor(k string, r *capturedRequest) []string {
	switch k {
	case "params":
		return r.bodyParams
	case "query_params":
		return r.queryParams
	case "header_names":
		return r.headerNames
	}
	return nil
}

func isInterfaceResource(t string) bool {
	switch strings.ToLower(t) {
	case "xhr", "fetch", "ping":
		return true
	}
	return false
}

func appendUnique(list []string, more ...string) []string {
	seen := map[string]bool{}
	out := append([]string{}, list...)
	for _, s := range out {
		seen[s] = true
	}
	for _, s := range more {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// cleanupChromeProfile kills any Chrome process still bound to this run's
// throwaway profile and removes the directory (crash/kill path included).
func cleanupChromeProfile(dir string) {
	if dir == "" {
		return
	}
	_ = exec.Command("pkill", "-9", "-f", dir).Run()
	_ = os.RemoveAll(dir)
}

// isStaticAssetPath keeps XHR/fetch-typed static resources out of interfaces.
func isStaticAssetPath(p string) bool {
	lp := strings.ToLower(p)
	for _, suf := range []string{".css", ".js", ".mjs", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".ico", ".woff", ".woff2", ".ttf", ".map", ".br", ".gz", ".wasm", ".webmanifest"} {
		if strings.HasSuffix(lp, suf) {
			return true
		}
	}
	for _, frag := range []string{"/webpack-artifacts/", "/_next/static/"} {
		if strings.Contains(lp, frag) {
			return true
		}
	}
	return false
}
