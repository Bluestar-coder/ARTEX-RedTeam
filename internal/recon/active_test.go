package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestActiveProviderScopeBeforeProbe(t *testing.T) {
	var mu sync.Mutex
	probed := map[string]bool{}
	probe := func(_ context.Context, host string) (probeResult, bool) {
		mu.Lock()
		probed[host] = true
		mu.Unlock()
		if host == "www.example.com" {
			status := 200
			return probeResult{StatusCode: status, RawRef: "http://" + host, Technologies: []string{"nginx"}}, true
		}
		return probeResult{}, false
	}
	ct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name_value": "www.example.com\napi.example.com"},
			{"name_value": "evil.other.com"},
		})
	}))
	defer ct.Close()
	cfg := Config{Concurrency: 2, HTTPTimeout: time.Second, CTURL: ct.URL, CTBackends: []string{"crt.sh"}, Wordlist: []string{"www", "mail"}}
	p := NewActiveProvider(cfg, ct.Client())
	p.probeFn = probe
	p.resolveFn = func(context.Context, string) []string { return nil }
	scope, _ := NewScope("example.com", nil, nil)
	res, err := p.Run(context.Background(), Request{Seed: "example.com", Scope: scope, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if probed["evil.other.com"] {
		t.Fatal("out-of-scope host was probed")
	}
	if !probed["www.example.com"] || !probed["api.example.com"] || !probed["mail.example.com"] {
		t.Fatalf("expected in-scope probes, got %v", probed)
	}
	// root + subdomains + one service; out-of-scope produces no subdomain either.
	var subs int
	for _, r := range res {
		if r.Type == TypeSubdomain {
			subs++
		}
	}
	if subs != 2 { // CT 的 www/api 保留；mail 是不解析的字典猜测，不落子域
		t.Fatalf("subdomains=%d records=%+v", subs, res)
	}
}

func TestActiveProbeSchemeFingerprint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			_, _ = w.Write([]byte("icon-bytes"))
			return
		}
		w.Header().Set("Server", "nginx")
		_, _ = w.Write([]byte(`<html><head><title>Hello</title><script src="/jquery.min.js"></script></head><body>ok</body></html>`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	cfg := Config{Concurrency: 1, HTTPTimeout: time.Second}
	p := NewActiveProvider(cfg, nil)
	res, ok := p.probeScheme(context.Background(), "http", host)
	if !ok {
		t.Fatal("probe failed")
	}
	if res.Title != "Hello" || res.StatusCode != 200 || res.FaviconMMH3 == "" {
		t.Fatalf("probe=%+v", res)
	}
	if len(res.Technologies) == 0 {
		t.Fatalf("no tech detected: %+v", res)
	}
}

// TestActiveProviderConcurrencyCap proves the configured concurrency limit is a
// hard cap (M1 acceptance: run with 2 and 20, never exceed the limit).
func TestActiveProviderConcurrencyCap(t *testing.T) {
	for _, limit := range []int{2, 20} {
		limit := limit
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			ct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entries := make([]map[string]any, 0, 40)
				for i := 0; i < 40; i++ {
					entries = append(entries, map[string]any{"name_value": fmt.Sprintf("s%d.example.com", i)})
				}
				_ = json.NewEncoder(w).Encode(entries)
			}))
			defer ct.Close()
			var inFlight, maxInFlight int32
			var mu sync.Mutex
			probe := func(ctx context.Context, host string) (probeResult, bool) {
				cur := atomic.AddInt32(&inFlight, 1)
				mu.Lock()
				if cur > atomic.LoadInt32(&maxInFlight) {
					atomic.StoreInt32(&maxInFlight, cur)
				}
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				atomic.AddInt32(&inFlight, -1)
				return probeResult{}, false
			}
			cfg := Config{Concurrency: limit, HTTPTimeout: time.Second, CTURL: ct.URL, CTBackends: []string{"crt.sh"}}
			p := NewActiveProvider(cfg, ct.Client())
			p.probeFn = probe
			p.resolveFn = func(context.Context, string) []string { return nil }
			scope, _ := NewScope("example.com", nil, nil)
			if _, err := p.Run(context.Background(), Request{Seed: "example.com", Scope: scope, Now: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			peak := atomic.LoadInt32(&maxInFlight)
			if peak > int32(limit) {
				t.Fatalf("peak concurrency %d > limit %d", peak, limit)
			}
			if peak == 0 {
				t.Fatal("no probes were recorded")
			}
			t.Logf("limit=%d peak=%d probes=40", limit, peak)
		})
	}
}

func TestActiveFallsBackToDictionaryWhenAllCTBackendsFail(t *testing.T) {
	probed := map[string]bool{}
	var mu sync.Mutex
	fail := &fakeCT{name: "a", err: context.DeadlineExceeded}
	fail2 := &fakeCT{name: "b", err: context.DeadlineExceeded}
	p := &ActiveProvider{
		ctBackends:  []CTBackend{fail, fail2},
		wordlist:    []string{"www"},
		concurrency: 1,
		timeout:     time.Second,
		probeFn: func(_ context.Context, host string) (probeResult, bool) {
			mu.Lock()
			probed[host] = true
			mu.Unlock()
			return probeResult{RawRef: "http://" + host}, true
		},
		resolveFn: func(context.Context, string) []string { return nil },
	}
	scope, _ := NewScope("example.com", nil, nil)
	res, err := p.Run(context.Background(), Request{Seed: "example.com", Scope: scope, Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("all-CT-fail must fall back to dictionary, not error: %v", err)
	}
	if !probed["www.example.com"] {
		t.Fatalf("dictionary candidate not probed: %v", probed)
	}
	foundSub, foundService := false, false
	for _, r := range res {
		if r.Type == TypeSubdomain && r.Value == "www.example.com" {
			foundSub = true
		}
		if r.Type == TypeService {
			foundService = true
		}
	}
	if !foundSub || !foundService {
		t.Fatalf("fallback records missing: %+v", res)
	}
}
