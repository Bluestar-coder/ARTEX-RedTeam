package recon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFOFAProviderParsesAndScopes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "good-key" {
			_, _ = w.Write([]byte(`{"error":true,"errmsg":"invalid key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":false,"size":3,"results":[
			["https://www.example.com:443","example.com","203.0.113.10","nginx","Welcome"],
			["api.example.com","example.com","203.0.113.11","OpenResty","API"],
			["evil.other.com","other.com","198.51.100.9","",""]
		]}`))
	}))
	defer srv.Close()
	cfg := FOFAConfig{Enabled: true, BaseURL: srv.URL, Email: "a@b.c", Key: "good-key", MaxResults: 10}
	p := NewFOFAProvider(cfg, srv.Client())
	scope, _ := NewScope("example.com", nil, nil)
	recs, err := p.Run(context.Background(), Request{Seed: "example.com", Scope: scope, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	var subs, services, ips int
	for _, r := range recs {
		switch r.Type {
		case TypeSubdomain:
			subs++
		case TypeService:
			services++
		case TypeIP:
			ips++
		}
		if r.Value == "evil.other.com" || r.Value == "198.51.100.9" {
			t.Fatalf("out-of-scope record emitted: %+v", r)
		}
	}
	if subs != 2 || services != 1 || ips != 2 {
		t.Fatalf("subs=%d services=%d ips=%d recs=%+v", subs, services, ips, recs)
	}

	// Wrong key must return an error (orchestrator degrades, does not fail the run).
	bad := cfg
	bad.Key = "bad-key"
	if _, err := NewFOFAProvider(bad, srv.Client()).Run(context.Background(), Request{Seed: "example.com", Scope: scope}); err == nil {
		t.Fatal("wrong key should error")
	}
	// Missing key is a configuration error, not a panic.
	if _, err := NewFOFAProvider(FOFAConfig{Enabled: true, BaseURL: srv.URL, Email: "a@b.c"}, nil).
		Run(context.Background(), Request{Seed: "example.com", Scope: scope}); err == nil {
		t.Fatal("missing key should error")
	}
}
