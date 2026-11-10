package recon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProviderDynamic is the runtime network-capture provider (default OFF). It is
// the only provider that interacts with a real browser; it is intentionally
// isolated from extract_js so an agent cannot trigger it accidentally.
const ProviderDynamic = "dynamic"

// DynamicConfig controls the headless-browser provider. Proxy is mandatory:
// browser traffic must leave through an existing egress proxy; when it is
// missing or unusable the provider fails rather than falling back to direct.
type DynamicConfig struct {
	Enabled     bool
	BrowserPath string        // chrome/chromium binary; empty = auto-detect
	Proxy       string        // http/https/socks5 egress; empty = unusable -> fail
	PageTimeout time.Duration // one page navigation timeout (default 30s)
	IdleWait    time.Duration // wait after load for async XHR (default 5s)
	HostTimeout time.Duration // whole provider/host budget (default 5m), multi-page runs
	MaxPages    int           // hard cap per run (default 10)
	MaxMemoryMB int           // JS heap cap passed to Chromium (default 1024)
	Concurrency int           // browser instances; default 1
}

// DynamicConfigFromEnv reads RECON_DYNAMIC_* (default disabled, proxy empty).
func dynamicEnv(suffix, def string) string {
	if v := env(suffix, ""); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(suffix))
}

func dynamicEnvInt(suffix string, def int) int {
	if v := dynamicEnv(suffix, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func dynamicEnvBool(suffix string, def bool) bool {
	if v := dynamicEnv(suffix, ""); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func DynamicConfigFromEnv() DynamicConfig {
	proxy := dynamicEnv("RECON_DYNAMIC_PROXY", "")
	if proxy == "" {
		// The server injects its egress proxy as HTTP(S)_PROXY for agents; use
		// that as the existing project egress rather than inventing a new one.
		for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				proxy = v
				break
			}
		}
	}
	return DynamicConfig{
		Enabled:     dynamicEnvBool("RECON_DYNAMIC_ENABLED", false),
		BrowserPath: dynamicEnv("RECON_DYNAMIC_BROWSER", ""),
		Proxy:       proxy,
		PageTimeout: time.Duration(dynamicEnvInt("RECON_DYNAMIC_PAGE_TIMEOUT_SEC", 30)) * time.Second,
		IdleWait:    time.Duration(dynamicEnvInt("RECON_DYNAMIC_IDLE_SEC", 5)) * time.Second,
		HostTimeout: time.Duration(dynamicEnvInt("RECON_DYNAMIC_HOST_TIMEOUT_SEC", 300)) * time.Second,
		MaxPages:    dynamicEnvInt("RECON_DYNAMIC_MAX_PAGES", 10),
		MaxMemoryMB: dynamicEnvInt("RECON_DYNAMIC_MAX_MEMORY_MB", 1024),
		Concurrency: dynamicEnvInt("RECON_DYNAMIC_CONCURRENCY", 1),
	}
}

func (c *DynamicConfig) applyDefaults() {
	if c.PageTimeout <= 0 {
		c.PageTimeout = 30 * time.Second
	}
	if c.IdleWait < 0 {
		c.IdleWait = 5 * time.Second
	}
	if c.HostTimeout <= 0 {
		c.HostTimeout = 5 * time.Minute
	}
	if c.MaxPages <= 0 {
		c.MaxPages = 10
	}
	if c.MaxMemoryMB <= 0 {
		c.MaxMemoryMB = 1024
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 1
	}
}

// DynamicProvider captures real browser requests for the seed page. It is
// registered as a normal provider but is disabled unless explicitly configured.
// dynamicBrowserSem is a process-wide hard cap on concurrent browser
// instances. The per-provider semaphore still enforces the configured limit;
// this one makes "default 1" true even when several tool calls create their own
// provider instances.
var dynamicBrowserSem = make(chan struct{}, 1)

type DynamicProvider struct {
	cfg DynamicConfig
	sem chan struct{}
}

// NewDynamicProvider builds the provider; registration is separate so the
// default factory can return nil when disabled.
func NewDynamicProvider(cfg DynamicConfig) *DynamicProvider {
	cfg.applyDefaults()
	return &DynamicProvider{cfg: cfg, sem: make(chan struct{}, cfg.Concurrency)}
}

func (p *DynamicProvider) Name() string { return ProviderDynamic }

func init() {
	defaultFactories = append(defaultFactories, func(cfg Config, _ *http.Client) Provider {
		if !cfg.Dynamic.Enabled {
			return nil
		}
		return NewDynamicProvider(cfg.Dynamic)
	})
}

// DynamicCapture is the detailed browser capture result. It is exposed so the
// dedicated extract_js_dynamic tool can persist facts without parsing records.
type DynamicCapture struct {
	PageURL      string
	Records      []Record
	Blocked      []BlockedRequest
	Observed     int
	XHRCount     int
	Duration     time.Duration
	BrowserPath  string
	Pages        int
	PagesVisited []string
	PageErrors   []string
	Proxy        string // credentials redacted
}

// BlockedRequest is one out-of-scope request stopped before leaving Chrome.
type BlockedRequest struct {
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// Run implements Provider. The seed URL uses the explicit scheme/port when the
// caller supplied one; otherwise it starts with HTTPS and falls back to HTTP
// when the page cannot load (a local Juice Shop or an internal HTTP service
// must not fail just because it has no certificate).
func (p *DynamicProvider) Run(ctx context.Context, req Request) ([]Record, error) {
	if !p.cfg.Enabled {
		return nil, fmt.Errorf("dynamic provider disabled (set RECON_DYNAMIC_ENABLED=1)")
	}
	host := req.Scope.Seed
	if host == "" {
		host = req.Seed
	}
	port := req.Scope.SeedPort
	schemes := []string{"https", "http"}
	if req.Scope.SeedScheme == "http" || req.Scope.SeedScheme == "https" {
		schemes = []string{req.Scope.SeedScheme}
	} else if port > 0 && port != 443 ||
		host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		// Local/internal targets are overwhelmingly HTTP and the failed HTTPS
		// attempt costs a page timeout; try HTTP first, HTTPS as fallback.
		schemes = []string{"http", "https"}
	}
	urlHost := host
	if port > 0 {
		urlHost = net.JoinHostPort(host, strconv.Itoa(port))
	} else if strings.Contains(host, ":") {
		urlHost = "[" + host + "]"
	}
	var lastErr error
	for i, scheme := range schemes {
		pageURL := scheme + "://" + urlHost + "/"
		cap, err := p.CapturePages(ctx, pageURL, req.Scope, nil)
		if err == nil {
			return cap.Records, nil
		}
		lastErr = err
		if i == len(schemes)-1 {
			break
		}
		log.Printf("[recon] provider=dynamic page=%s scheme failed (%v), retrying %s", pageURL, err, schemes[i+1])
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("dynamic capture failed")
	}
	return nil, lastErr
}

// Capture loads just the home page (backwards-compatible single-page path).
func (p *DynamicProvider) Capture(ctx context.Context, pageURL string, scope Scope) (DynamicCapture, error) {
	return p.CapturePages(ctx, pageURL, scope, nil)
}

// CapturePages loads the home page plus known same-host page paths, then follows
// same-host links found in the home page DOM. It only navigates: no clicks, no
// forms, no login. Out-of-scope requests are refused by the scope proxy before
// they are sent.
func (p *DynamicProvider) CapturePages(ctx context.Context, pageURL string, scope Scope, known []string) (DynamicCapture, error) {
	out := DynamicCapture{PageURL: pageURL, Proxy: redactProxy(p.cfg.Proxy)}
	if !p.cfg.Enabled {
		return out, fmt.Errorf("dynamic provider disabled (set RECON_DYNAMIC_ENABLED=1)")
	}
	if strings.TrimSpace(p.cfg.Proxy) == "" {
		return out, fmt.Errorf("dynamic provider requires an egress proxy (set RECON_DYNAMIC_PROXY or HTTP(S)_PROXY); refusing direct connection")
	}
	if _, err := validateDynamicProxy(p.cfg.Proxy); err != nil {
		return out, fmt.Errorf("dynamic proxy invalid: %w", err)
	}
	u, err := url.Parse(pageURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return out, fmt.Errorf("invalid page URL %q", pageURL)
	}
	if ok, reason := scope.Allow(u.Hostname()); !ok {
		return out, fmt.Errorf("page host out of scope: %s", reason)
	}
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return out, ctx.Err()
	}
	select {
	case dynamicBrowserSem <- struct{}{}:
		defer func() { <-dynamicBrowserSem }()
	case <-ctx.Done():
		return out, ctx.Err()
	}
	hostCtx, cancel := context.WithTimeout(ctx, p.cfg.HostTimeout)
	defer cancel()
	started := time.Now()
	cap, err := captureBrowser(hostCtx, pageURL, scope, p.cfg, known)
	out.Duration = time.Since(started)
	if cap.PageURL == "" {
		out.PageURL = pageURL
	}
	out.Records = cap.Records
	out.Blocked = cap.Blocked
	out.Observed = cap.Observed
	out.XHRCount = cap.XHRCount
	out.BrowserPath = cap.BrowserPath
	out.Pages = cap.Pages
	out.PagesVisited = cap.PagesVisited
	out.PageErrors = cap.PageErrors
	log.Printf("[recon] provider=dynamic page=%s duration=%s pages=%d max_pages=%d max_memory_mb=%d concurrency=%d processes=1 observed=%d xhr_fetch=%d blocked=%d browser=%q proxy=%s err=%v",
		pageURL, out.Duration.Round(time.Millisecond), out.Pages, p.cfg.MaxPages, p.cfg.MaxMemoryMB, p.cfg.Concurrency, out.Observed, out.XHRCount, len(out.Blocked), out.BrowserPath, out.Proxy, err)
	if len(out.PagesVisited) > 0 {
		log.Printf("[recon] provider=dynamic visited=%s", strings.Join(out.PagesVisited, ","))
	}
	for _, pe := range out.PageErrors {
		log.Printf("[recon] provider=dynamic page_error=%s", pe)
	}
	for _, b := range out.Blocked {
		log.Printf("[recon] provider=dynamic blocked url=%s reason=%s", b.URL, b.Reason)
	}
	if len(cap.Records) == 0 {
		log.Printf("[recon] provider=dynamic page=%s 未捕获 xhr/fetch（静态/运行时接口可能不在本次页面路径上）", pageURL)
	}
	return out, err
}

func validateDynamicProxy(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid proxy URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	}
	return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
}

func redactProxy(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return u.String()
}

func findChromePath(configured string) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
		if p, err := exec.LookPath(configured); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("configured browser not found: %s", configured)
	}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome",
	}
	for _, c := range candidates {
		if strings.HasPrefix(c, "/") {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no chromium/chrome binary found: install Chromium or set RECON_DYNAMIC_BROWSER")
}
