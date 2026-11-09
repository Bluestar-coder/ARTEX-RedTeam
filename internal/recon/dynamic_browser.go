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
func captureBrowser(ctx context.Context, pageURL string, scope Scope, cfg DynamicConfig) (DynamicCapture, error) {
	out := DynamicCapture{PageURL: pageURL, Pages: 1, Proxy: redactProxy(cfg.Proxy)}
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

	state := newCaptureState(scope)
	state.pageURL = pageURL
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx, chromedp.WithListener(func(ev any) {
		state.handle(ev)
	}))
	defer cancelBrowser()

	if _, err := chromedp.Call(browserCtx, network.Enable, network.EnableParams{}); err != nil {
		return out, fmt.Errorf("network enable failed: %w", err)
	}
	navCtx, cancelNav := context.WithTimeout(browserCtx, cfg.PageTimeout)
	navErr := chromedp.Do(navCtx, chromedp.Navigate(pageURL))
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

	// Best-effort body parameter names for requests whose body was not inlined in
	// the event. Values are never stored; paramNames returns names only.
	if c := chromedp.FromContext(browserCtx); c != nil && c.Target != nil {
		state.fillPostData(c.Target, browserCtx)
	}
	out.Records = state.records()
	out.Blocked = sp.blockedList()
	out.Observed = state.observedCount()
	out.XHRCount = len(out.Records)
	if navErr != nil {
		return out, fmt.Errorf("page load failed: %w", navErr)
	}
	return out, nil
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
	mu       sync.Mutex
	scope    Scope
	pageURL  string
	requests map[string]*capturedRequest
	order    []string
	observed int
	loaded   bool
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
				"page_url": s.pageURL, "source": "dynamic",
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
	for _, suf := range []string{".css", ".js", ".mjs", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".ico", ".woff", ".woff2", ".ttf", ".map"} {
		if strings.HasSuffix(lp, suf) {
			return true
		}
	}
	return false
}
