package recon

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// localForwardProxy is a test-only CONNECT/HTTP proxy so browser egress has a
// real proxy hop instead of a direct connection. Production uses the project
// egress proxy (RECON_DYNAMIC_PROXY or HTTP(S)_PROXY).
type localForwardProxy struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []string
}

func newLocalForwardProxy() *localForwardProxy {
	p := &localForwardProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = append(p.seen, r.Method+" "+r.Host)
		p.mu.Unlock()
		if r.Method == http.MethodConnect {
			dst, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				dst.Close()
				http.Error(w, "no hijack", http.StatusInternalServerError)
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				dst.Close()
				return
			}
			_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
			go func() { _, _ = io.Copy(dst, conn); dst.Close() }()
			go func() { _, _ = io.Copy(conn, dst); conn.Close() }()
			return
		}
		tr := &http.Transport{}
		defer tr.CloseIdleConnections()
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for k, vv := range r.Header {
			for _, v := range vv {
				out.Header.Add(k, v)
			}
		}
		resp, err := tr.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	return p
}

func (p *localForwardProxy) URL() string { return p.srv.URL }
func (p *localForwardProxy) Close()      { p.srv.Close() }

func TestDynamicCloudflareE2E(t *testing.T) {
	if os.Getenv("DYNAMIC_E2E") != "1" {
		t.Skip("set DYNAMIC_E2E=1 with Chrome installed to run browser capture")
	}
	proxy := newLocalForwardProxy()
	defer proxy.Close()
	scope, err := NewScope("www.cloudflare.com", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DynamicConfig{Enabled: true, Proxy: proxy.URL(), PageTimeout: 30 * time.Second, IdleWait: 6 * time.Second, HostTimeout: 2 * time.Minute, MaxPages: 1, MaxMemoryMB: 512, Concurrency: 1}
	p := NewDynamicProvider(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cap, err := p.Capture(ctx, "https://www.cloudflare.com/", scope)
	if err != nil {
		t.Logf("capture error=%v blocked=%d", err, len(cap.Blocked))
	}
	if cap.Observed == 0 {
		t.Fatalf("browser observed no requests: %+v", cap)
	}
	t.Logf("observed=%d xhr_fetch=%d blocked=%d proxy=%s", cap.Observed, len(cap.Records), len(cap.Blocked), cap.Proxy)
	for _, r := range cap.Records {
		t.Logf("DYN %s %s | params=%v query=%v headers=%v status=%v ct=%v", r.Extra["method"], r.Value, r.Extra["params"], r.Extra["query_params"], r.Extra["header_names"], r.Extra["status"], r.Extra["content_type"])
		if host := r.Parent; host != "www.cloudflare.com" && !strings.HasSuffix(host, ".cloudflare.com") {
			t.Errorf("out-of-scope record leaked: %v", r)
		}
	}
	if len(cap.Records) < 3 {
		t.Fatalf("want >=3 real interfaces on cloudflare, got %d", len(cap.Records))
	}
	if len(cap.Blocked) == 0 {
		t.Fatalf("expected blocked out-of-scope requests")
	}
	if !proxySaw(proxy) {
		t.Fatalf("proxy saw no traffic; browser may have gone direct")
	}
}

func proxySaw(p *localForwardProxy) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen) > 0
}

func TestDynamicPageTimeout(t *testing.T) {
	if os.Getenv("DYNAMIC_E2E") != "1" {
		t.Skip("set DYNAMIC_E2E=1 with Chrome installed to run browser capture")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	stop := make(chan struct{})
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			// Accept the TCP connection but never answer CONNECT: the local scope
			// proxy waits on the upstream until the page timeout fires.
		}
	}()
	defer func() {
		close(stop)
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	}()
	scope, _ := NewScope("example.com", nil, nil)
	cfg := DynamicConfig{Enabled: true, Proxy: "http://" + ln.Addr().String(), PageTimeout: 1 * time.Second, IdleWait: 0, HostTimeout: 10 * time.Second, MaxPages: 1, MaxMemoryMB: 256, Concurrency: 1}
	p := NewDynamicProvider(cfg)
	start := time.Now()
	_, err = p.Capture(context.Background(), "https://example.com/", scope)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("stalled page load must fail at PageTimeout")
	}
	if elapsed > 15*time.Second {
		t.Fatalf("timeout not enforced: %s", elapsed)
	}
	t.Logf("page timeout ok: elapsed=%s err=%v", elapsed, err)
}

func TestDynamicUnavailableProxyNoDirect(t *testing.T) {
	if os.Getenv("DYNAMIC_E2E") != "1" {
		t.Skip("set DYNAMIC_E2E=1 with Chrome installed to run browser capture")
	}
	scope, _ := NewScope("example.com", nil, nil)
	cfg := DynamicConfig{Enabled: true, Proxy: "http://127.0.0.1:1", PageTimeout: 2 * time.Second, IdleWait: 0, HostTimeout: 10 * time.Second, MaxPages: 1, MaxMemoryMB: 256, Concurrency: 1}
	p := NewDynamicProvider(cfg)
	start := time.Now()
	_, err := p.Capture(context.Background(), "https://example.com/", scope)
	if err == nil {
		t.Fatal("unavailable proxy must fail, not fall back to direct")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("unavailable proxy took too long: %s", elapsed)
	}
	t.Logf("unavailable proxy failed as expected: %v", err)
}

func TestDynamicKillCleanup(t *testing.T) {
	if os.Getenv("DYNAMIC_E2E") != "1" {
		t.Skip("set DYNAMIC_E2E=1 with Chrome installed to run browser kill/cleanup test")
	}
	proxy := newLocalForwardProxy()
	defer proxy.Close()
	scope, _ := NewScope("www.cloudflare.com", nil, nil)
	cfg := DynamicConfig{Enabled: true, Proxy: proxy.URL(), PageTimeout: 30 * time.Second, IdleWait: 8 * time.Second, HostTimeout: 2 * time.Minute, MaxPages: 1, MaxMemoryMB: 512, Concurrency: 1}
	p := NewDynamicProvider(cfg)
	done := make(chan error, 1)
	go func() {
		_, err := p.Capture(context.Background(), "https://www.cloudflare.com/", scope)
		done <- err
	}()
	// Wait for our Chrome process (unique artex-dynamic-* profile) then kill it.
	var pid string
	for i := 0; i < 100; i++ {
		out, _ := exec.Command("pgrep", "-f", "artex-dynamic-").Output()
		fields := strings.Fields(string(out))
		if len(fields) > 0 {
			pid = fields[0]
			break
		}
		select {
		case <-done:
			t.Fatal("capture finished before Chrome could be killed")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if pid == "" {
		t.Fatal("could not find artex-dynamic Chrome process to kill")
	}
	// Let the page actually start loading before killing the browser.
	time.Sleep(2 * time.Second)
	if err := exec.Command("kill", "-9", pid).Run(); err != nil {
		t.Fatalf("kill chrome pid %s: %v", pid, err)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("capture did not return after browser kill")
	}
	time.Sleep(1500 * time.Millisecond)
	out, _ := exec.Command("pgrep", "-f", "artex-dynamic-").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("residual Chrome processes after kill: %s", out)
	}
	t.Logf("killed chrome pid=%s; no residual artex-dynamic processes", pid)
}
