package jsintel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
)

func TestFetcherScansScriptsAndEnforcesScope(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<html><body>
<script src="/a.js"></script>
<script src="/b.js"></script>
<script src="http://other.example/out.js"></script>
<script>fetch("/api/inline", {method:"POST"})</script>
<script type="application/json">{"ignored": true}</script>
</body></html>`))
	})
	mux.HandleFunc("/a.js", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`fetch("/api/users", {method:"GET"});`))
	})
	mux.HandleFunc("/b.js", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`axios.post("/api/users", {name: n});\nfetch("/api/other");`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	scope, err := recon.NewScope("127.0.0.1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := NewFetcher(scope, 4, 5*time.Second)
	agg := f.Scan(context.Background(), []Target{{Host: "127.0.0.1", PageURL: srv.URL + "/", AssetID: 1}})
	if agg.Pages != 1 || agg.JSFiles != 3 { // a.js, b.js, inline
		t.Fatalf("pages=%d jsfiles=%d skipped=%+v", agg.Pages, agg.JSFiles, agg.Skipped)
	}
	if agg.Bytes == 0 || len(agg.Files) != 2 {
		t.Fatalf("bytes=%d files=%+v", agg.Bytes, agg.Files)
	}
	var getUsers, postUsers Interface
	foundInline := false
	for _, it := range agg.Interfaces {
		if it.Path == "/api/users" && it.Method == "GET" {
			getUsers = it
		}
		if it.Path == "/api/users" && it.Method == "POST" {
			postUsers = it
		}
		if it.Path == "/api/inline" && it.Method == "POST" {
			foundInline = true
		}
	}
	if getUsers.Path == "" || len(getUsers.Sources) != 1 || !strings.Contains(getUsers.Sources[0].ScriptURL, "a.js") {
		t.Fatalf("GET users interface=%+v", getUsers)
	}
	if postUsers.Path == "" || len(postUsers.Sources) != 1 || !strings.Contains(postUsers.Sources[0].ScriptURL, "b.js") || !contains(postUsers.Params, "name") {
		t.Fatalf("POST users interface=%+v", postUsers)
	}
	if !foundInline {
		t.Fatalf("inline POST not extracted: %+v", agg.Interfaces)
	}
	outs := false
	for _, s := range agg.Skipped {
		if strings.Contains(s.URL, "other.example") && strings.Contains(s.Reason, "out_of_scope") {
			outs = true
		}
	}
	if !outs {
		t.Fatalf("out-of-scope script should be skipped before fetch: %+v", agg.Skipped)
	}
}

func TestFetcherHostBudgetCap(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<script src="/a.js"></script><script src="/b.js"></script>`))
	})
	js := strings.Repeat("var x=1;", 8)
	mux.HandleFunc("/a.js", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(js)) })
	mux.HandleFunc("/b.js", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(js)) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	scope, _ := recon.NewScope("127.0.0.1", nil, nil)
	f := NewFetcher(scope, 2, 5*time.Second)
	f.MaxFileBytes = 4096
	f.MaxHostBytes = 60 // page itself is ~62 bytes, leaving no room for scripts
	agg := f.Scan(context.Background(), []Target{{Host: "127.0.0.1", PageURL: srv.URL + "/"}})
	hostSkip := false
	for _, s := range agg.Skipped {
		if strings.Contains(s.Reason, "host_budget_exceeded") {
			hostSkip = true
		}
	}
	if !hostSkip {
		t.Fatalf("host budget not enforced: %+v", agg.Skipped)
	}
}

func TestFetcherFileSizeCap(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<script src="/big.js"></script>`))
	})
	mux.HandleFunc("/big.js", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("var x=1;", 100)))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	scope, _ := recon.NewScope("127.0.0.1", nil, nil)
	f := NewFetcher(scope, 2, 5*time.Second)
	f.MaxFileBytes = 64 // page itself is small; big.js exceeds this
	agg := f.Scan(context.Background(), []Target{{Host: "127.0.0.1", PageURL: srv.URL + "/"}})
	skipped := false
	for _, s := range agg.Skipped {
		if strings.Contains(s.URL, "big.js") && strings.Contains(s.Reason, "file_too_large") {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("oversized JS not skipped: %+v", agg.Skipped)
	}
}
