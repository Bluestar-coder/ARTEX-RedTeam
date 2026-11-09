package agent

import (
	"context"
	"encoding/json"
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

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/jsintel"
)

// e2eForwardProxy is a test-only upstream egress proxy. Chrome talks to the
// provider's local scope proxy, which chains CONNECT here; the browser never
// dials the target directly.
type e2eForwardProxy struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []string
}

func newE2EForwardProxy() *e2eForwardProxy {
	p := &e2eForwardProxy{}
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
		out, _ := http.NewRequest(r.Method, r.URL.String(), r.Body)
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

func (p *e2eForwardProxy) URL() string { return p.srv.URL }
func (p *e2eForwardProxy) Close()      { p.srv.Close() }

func TestExtractJSDynamicE2E(t *testing.T) {
	if os.Getenv("DYNAMIC_E2E") != "1" {
		t.Skip("set DYNAMIC_E2E=1 with Chrome installed to run dynamic extraction; CI without a browser skips")
	}
	if _, err := exec.LookPath("google-chrome"); err != nil {
		// macOS path is auto-detected by the provider; only fail if nothing exists.
		if _, statErr := os.Stat("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"); statErr != nil {
			t.Skip("no chrome/chromium installed")
		}
	}
	proxy := newE2EForwardProxy()
	defer proxy.Close()
	t.Setenv("RECON_DYNAMIC_ENABLED", "1")
	t.Setenv("RECON_DYNAMIC_PROXY", proxy.URL())
	t.Setenv("HTTPS_PROXY", proxy.URL())
	t.Setenv("HTTP_PROXY", proxy.URL())
	t.Setenv("RECON_DYNAMIC_PAGE_TIMEOUT_SEC", "30")
	t.Setenv("RECON_DYNAMIC_IDLE_SEC", "5")

	d := testDB(t)
	defer d.Close()
	expID, err := d.CreateExploration("m3 dynamic e2e", "capture cloudflare requests")
	if err != nil {
		t.Fatal(err)
	}
	tsx := NewToolSet(d.Exploration(expID), "planner")
	tsx.SetAssetStore(d.Assets(), d.Companies())
	call := func() map[string]any {
		raw, _ := json.Marshal(map[string]any{"domain": "www.cloudflare.com"})
		res, err := tsx.extractJSDynamic().Call(context.Background(), raw, nil)
		if err != nil {
			t.Fatalf("extract_js_dynamic: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(res.Flatten()), &out); err != nil {
			t.Fatalf("result=%s err=%v", res.Flatten(), err)
		}
		return out
	}
	first := call()
	t.Logf("dynamic first run: %v", first)
	if int(first["interfaces"].(float64)) < 3 {
		t.Fatalf("want >=3 real dynamic interfaces on cloudflare, got %v", first)
	}
	if pc, _ := first["pages_count"].(float64); pc < 5 {
		t.Fatalf("want multi-page run (>=5 pages), got %v", first["pages"])
	}
	facts := func() []map[string]any {
		rows, err := d.Query(`SELECT payload FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND payload->>'extract_kind'='js_interface' ORDER BY id`, expID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var p map[string]any
			_ = json.Unmarshal(raw, &p)
			out = append(out, p)
		}
		return out
	}
	fs := facts()
	if len(fs) < 3 {
		t.Fatalf("stored dynamic facts=%d", len(fs))
	}
	maxSources := 0
	for _, p := range fs {
		if n, ok := p["source_count"].(float64); ok && int(n) > maxSources {
			maxSources = int(n)
		}
	}
	if maxSources < 2 {
		t.Fatalf("dynamic facts should accumulate page sources across pages, max source_count=%d", maxSources)
	}
	for _, p := range fs {
		t.Logf("DYN FACT %s %s %s source=%v conf=%v params=%v headers=%v", p["method"], p["path"], p["host"], p["source"], p["extract_confidence"], p["params"], p["header_names"])
	}
	// Add a static observation for one dynamic interface, then run again: the
	// same extract_key must merge both sources (no duplicate fact row).
	firstPath, _ := fs[0]["path"].(string)
	firstMethod, _ := fs[0]["method"].(string)
	firstHost, _ := fs[0]["host"].(string)
	staticSrc := jsintel.SourceRef{PageURL: "https://www.cloudflare.com/", ScriptURL: "https://www.cloudflare.com/static/app.js", Source: "static", Snippet: "static fetch(\"" + firstPath + "\")"}
	_, _, err = d.Exploration(expID).UpsertExtractedFact("jsif|"+firstHost+"|"+firstMethod+"|"+strings.ToLower(firstPath), map[string]any{
		"extract_kind": "js_interface", "source": "static", "host": firstHost, "method": firstMethod, "path": firstPath,
		"extract_confidence": "high", "params": []string{}, "tags": []string{},
		"sources": []jsintel.SourceRef{staticSrc}, "source_count": 1, "observed_at": time.Now().UTC().Format(time.RFC3339),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := call()
	if int(second["facts_created"].(float64)) != 0 {
		t.Fatalf("idempotency: second dynamic run created new facts: %v", second)
	}
	fs = facts()
	for _, p := range fs {
		if p["path"] != firstPath || p["method"] != firstMethod {
			continue
		}
		raw, _ := json.Marshal(p["sources"])
		t.Logf("MERGED path=%s sources=%s kinds=%v", p["path"], raw, payloadSourceKinds(p))
		if !strings.Contains(string(raw), "static") || !strings.Contains(string(raw), "dynamic") {
			t.Fatalf("merged sources missing: %s", raw)
		}
		if int(p["source_count"].(float64)) < 2 {
			t.Fatalf("source_count not merged: %v", p["source_count"])
		}
	}
	ov := tsx.graphOverviewData()
	js, _ := ov["js_intel"].(map[string]any)
	raw, _ := json.MarshalIndent(js, "", "  ")
	t.Logf("graph_overview.js_intel:\n%s", raw)
	by, _ := js["by_source"].(map[string]int)
	if by["dynamic"] == 0 {
		t.Fatalf("by_source missing dynamic: %v", js)
	}
	t.Logf("by_source=%v (merged fact counts in both)", by)
}
