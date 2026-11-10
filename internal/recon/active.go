package recon

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ActiveProvider is the P0 active probe source: certificate transparency plus a
// tiny built-in dictionary for enumeration, then concurrent HTTP probing for
// status/title/favicon/technologies. Every candidate is scope-checked before any
// request is issued.
type ActiveProvider struct {
	client      *http.Client
	ctClient    *http.Client // CT queries can be slow; separate longer timeout
	ctBackends  []CTBackend
	wordlist    []string
	concurrency int
	timeout     time.Duration

	// Test hooks (nil = real network). They keep unit tests hermetic and let the
	// scope test prove out-of-scope hosts never reach a probe.
	probeFn   func(ctx context.Context, host string) (probeResult, bool)
	resolveFn func(ctx context.Context, host string) []string
}

type probeResult struct {
	Scheme        string
	StatusCode    int
	ContentLength int64
	Title         string
	FaviconMMH3   string
	Technologies  []string
	RawRef        string
}

// NewActiveProvider builds the provider from config.
func NewActiveProvider(cfg Config, client *http.Client) *ActiveProvider {
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 10
	}
	if concurrency > 32 {
		concurrency = 32
	}
	p := &ActiveProvider{
		client:      client,
		ctClient:    &http.Client{Timeout: 45 * time.Second},
		ctBackends:  CTBackendDefs(cfg, &http.Client{Timeout: 45 * time.Second}),
		wordlist:    append([]string(nil), cfg.Wordlist...),
		concurrency: concurrency,
		timeout:     timeout,
	}
	if p.client == nil {
		p.client = newProbeClient(timeout)
	}
	return p
}

func (p *ActiveProvider) Name() string { return ProviderActive }

// probeClient is separate from the plain client used for CT/API calls: TLS
// verification is intentionally skipped (pentest probe against self-signed
// targets) and redirects are not followed (a redirect could leave scope).
func newProbeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // pentest probe
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (p *ActiveProvider) Run(ctx context.Context, req Request) ([]Record, error) {
	seed := req.Scope.Seed
	if seed == "" {
		seed = HostOf(req.Seed)
	}
	if seed == "" {
		return nil, fmt.Errorf("active: empty seed")
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	seedIsIP := net.ParseIP(seed) != nil
	seedType := TypeRootDomain
	if seedIsIP {
		seedType = TypeIP
	}
	out := []Record{{
		Type: seedType, Value: seed, Parent: seed,
		Source: "active:seed", ObservedAt: now, RawRef: "seed",
	}}

	candidates := map[string]string{seed: "active:seed"}

	// CT and the dictionary only make sense for a registrable DNS name. An IP
	// literal or a single-label host (localhost) is probed directly; appending
	// dictionary words to it would only create nonsense candidates.
	dnsSeed := !seedIsIP && strings.Contains(seed, ".")
	ctFirst := map[string]time.Time{}
	if dnsSeed {
		// 1) Certificate transparency (backend order is configurable; failures
		// fall through to the next backend, and all-failed falls back to the
		// dictionary).
		if names, backend, err := p.enumerateCT(ctx, seed); err != nil {
			logProviderf(p.Name(), "所有 CT 后端均失败（降级，继续用内置字典）: %v", err)
		} else {
			src := "active:ct:" + backend
			for _, n := range names {
				if !n.FirstSeen.IsZero() {
					ctFirst[n.Name] = n.FirstSeen
				}
				p.addCandidate(candidates, req.Scope, n.Name, src, now, &out)
			}
		}

		// 2) Tiny built-in dictionary (allowed "CT logs + built-in dictionary").
		for _, word := range p.wordlist {
			host := strings.ToLower(strings.TrimSpace(word)) + "." + seed
			p.addCandidate(candidates, req.Scope, host, "active:dict", now, &out)
		}
	}

	// 3) Concurrent HTTP probe (bounded worker pool).
	type probeJob struct {
		host   string
		source string
		extra  map[string]any
	}
	jobs := make([]probeJob, 0, len(candidates))
	for host, src := range candidates {
		job := probeJob{host: host, source: src}
		if first, ok := ctFirst[host]; ok {
			job.extra = map[string]any{"ct_first_seen": first.UTC().Format(time.RFC3339)}
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].host < jobs[j].host })

	sem := make(chan struct{}, p.concurrency)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(job probeJob) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			res, alive := p.probeHost(ctx, job.host, req.Scope.SeedPort)
			ips := p.resolve(ctx, job.host)
			mu.Lock()
			defer mu.Unlock()
			// Subdomain provenance differs by枚举 source:
			//   - CT/DNS names are passive evidence: record them even if currently
			//     dead (the certificate historically existed);
			//   - dictionary guesses are only assets once they resolve or answer
			//     HTTP, otherwise every unregistered word would pollute the graph.
			emitSub := job.host != seed && (job.source != "active:dict" || alive || len(ips) > 0)
			if emitSub {
				out = append(out, Record{
					Type: TypeSubdomain, Value: job.host, Parent: seed,
					Source: job.source, ObservedAt: now,
					RawRef: "enumerate:" + job.source, Extra: job.extra,
				})
			}
			if alive {
				status := res.StatusCode
				length := res.ContentLength
				out = append(out, Record{
					Type: TypeService, Value: res.RawRef, Parent: seed,
					Source: "active:http", ObservedAt: now, RawRef: res.RawRef,
					StatusCode: &status, ContentLength: &length,
					Title: res.Title, FaviconMMH3: res.FaviconMMH3, Technologies: res.Technologies,
				})
			}
			for _, ip := range ips {
				out = append(out, Record{
					Type: TypeIP, Value: ip, Parent: job.host,
					Source: "active:dns", ObservedAt: now, RawRef: "dns:" + job.host,
				})
			}
		}(job)
	}
	wg.Wait()
	return foldWildcardRecords(out, seed), nil
}

// addCandidate scope-checks a candidate before it can ever be probed. Out-of-
// scope values are dropped here with a log line (provider-side enforcement).
func (p *ActiveProvider) addCandidate(candidates map[string]string, scope Scope, raw, source string, now time.Time, out *[]Record) {
	host := HostOf(raw)
	if host == "" {
		log.Printf("[recon] 丢弃非法候选 provider=%s value=%q reason=invalid host at=%s", p.Name(), raw, now.Format(time.RFC3339))
		return
	}
	if ok, reason := scope.Allow(host); !ok {
		log.Printf("[recon] 丢弃越界候选 provider=%s value=%q reason=%s at=%s", p.Name(), host, reason, now.Format(time.RFC3339))
		return
	}
	if _, exists := candidates[host]; !exists {
		candidates[host] = source
	}
}

// enumerateCT tries backends in configured order. It returns the first
// successful result together with the backend name; every failure is logged and
// the next backend is attempted.
func (p *ActiveProvider) enumerateCT(ctx context.Context, seed string) ([]CTName, string, error) {
	if len(p.ctBackends) == 0 {
		return nil, "", fmt.Errorf("没有可用的 CT 后端")
	}
	var errs []string
	for _, backend := range p.ctBackends {
		names, err := backend.Lookup(ctx, seed)
		if err != nil {
			logProviderf(p.Name(), "CT 后端 %s 失败，切换下一个: %v", backend.Name(), err)
			errs = append(errs, backend.Name()+": "+err.Error())
			continue
		}
		logProviderf(p.Name(), "CT 后端 %s 返回 %d 条名称", backend.Name(), len(names))
		return names, backend.Name(), nil
	}
	return nil, "", fmt.Errorf("%s", strings.Join(errs, "; "))
}

// probeHost applies the seed port (host:port / URL input) and delegates to the
// ordinary probe or the hermetic test hook. The hook sees the host:port form so
// tests can assert that 127.0.0.1:3000 / [::1]:8080 keep their port.
func (p *ActiveProvider) probeHost(ctx context.Context, host string, port int) (probeResult, bool) {
	target := host
	if port > 0 {
		target = net.JoinHostPort(host, strconv.Itoa(port))
	}
	if p.probeFn != nil {
		return p.probeFn(ctx, target)
	}
	return p.probeTarget(ctx, target)
}

// probe tries HTTPS then HTTP and extracts status/title/favicon/technologies.
func (p *ActiveProvider) probe(ctx context.Context, host string) (probeResult, bool) {
	return p.probeTarget(ctx, host)
}

func (p *ActiveProvider) probeTarget(ctx context.Context, host string) (probeResult, bool) {
	if p.probeFn != nil {
		return p.probeFn(ctx, host)
	}
	for _, scheme := range []string{"https", "http"} {
		res, ok := p.probeScheme(ctx, scheme, host)
		if ok {
			return res, true
		}
	}
	return probeResult{}, false
}

// hostForURL brackets a bare IPv6 literal; host:port and IPv4 are unchanged.
func hostForURL(host string) string {
	if strings.HasPrefix(host, "[") || net.ParseIP(host) == nil {
		return host
	}
	return "[" + host + "]"
}

func (p *ActiveProvider) probeScheme(parent context.Context, scheme, host string) (probeResult, bool) {
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	raw := scheme + "://" + hostForURL(host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return probeResult{}, false
	}
	req.Header.Set("User-Agent", "artex-redteam-recon/1.0")
	resp, err := p.client.Do(req)
	if err != nil {
		return probeResult{}, false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, responseLimit))
	res := probeResult{
		Scheme: scheme, StatusCode: resp.StatusCode, ContentLength: int64(len(body)),
		Title:        extractTitle(body),
		Technologies: detectTechnologies(resp.Header, body),
		RawRef:       raw,
	}
	if fav, err := p.fetchFavicon(parent, raw+"/favicon.ico"); err == nil && len(fav) > 0 {
		res.FaviconMMH3 = faviconMMH3(fav)
	}
	return res, true
}

func (p *ActiveProvider) fetchFavicon(parent context.Context, raw string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-redteam-recon/1.0")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("favicon HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 256<<10))
}

func (p *ActiveProvider) resolve(ctx context.Context, host string) []string {
	if p.resolveFn != nil {
		return p.resolveFn(ctx, host)
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	ips, err := netLookupHost(ctx, host)
	if err != nil {
		return nil
	}
	return ips
}

func init() {
	defaultFactories = append(defaultFactories, func(cfg Config, client *http.Client) Provider {
		return NewActiveProvider(cfg, client)
	})
}
