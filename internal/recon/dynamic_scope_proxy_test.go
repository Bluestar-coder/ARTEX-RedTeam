package recon

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestScopeProxyBlocksBeforeUpstream(t *testing.T) {
	// Upstream deliberately unreachable: an out-of-scope request must be blocked
	// by the local proxy before any dial is attempted.
	sp, err := newScopeProxy("http://127.0.0.1:1", mustScope(t, "seed.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	proxyURL, _ := url.Parse(sp.URL())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 3 * time.Second}
	resp, err := client.Get("http://out-of-scope.example/path")
	if err != nil {
		t.Fatalf("blocked request should return an HTTP 403, not a dial error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("blocked status=%d want 403", resp.StatusCode)
	}
	blocked := sp.blockedList()
	if len(blocked) != 1 || !strings.Contains(blocked[0].URL, "out-of-scope.example") {
		t.Fatalf("blocked list=%v", blocked)
	}
}

func mustScope(t *testing.T, seed string) Scope {
	t.Helper()
	s, err := NewScope(seed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
