package db

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
)

type e2eProvider struct {
	name string
	recs []recon.Record
	err  error
}

func (p e2eProvider) Name() string { return p.name }
func (p e2eProvider) Run(context.Context, recon.Request) ([]recon.Record, error) {
	return p.recs, p.err
}

func toDBRecon(records []recon.Record) []ReconAsset {
	out := make([]ReconAsset, 0, len(records))
	for _, r := range records {
		out = append(out, ReconAsset{
			Type: string(r.Type), Value: r.Value, Parent: r.Parent, Source: r.Source,
			ObservedAt: r.ObservedAt, RawRef: r.RawRef, IP: r.IP,
			StatusCode: r.StatusCode, ContentLength: r.ContentLength, Title: r.Title,
			FaviconMMH3: r.FaviconMMH3, Technologies: r.Technologies,
		})
	}
	return out
}

// TestReconE2E is the real-network M1 acceptance test. It is skipped by default
// (CI never needs the public internet): set RECON_E2E_DOMAIN=example.com and a
// PostgreSQL DSN to run it, e.g. in the manual verification workflow.
func TestReconE2E(t *testing.T) {
	seed := strings.TrimSpace(os.Getenv("RECON_E2E_DOMAIN"))
	if seed == "" {
		t.Skip("set RECON_E2E_DOMAIN to run real-network recon acceptance")
	}
	d := openTestDB(t)
	as := d.Assets()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	scope, err := recon.NewScope(seed, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := recon.ConfigFromEnv()
	countAssets := func() int {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM assets WHERE source <> ''`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1) Three runs: the first is the real discovery run; runs 2/3 keep the exact
	// same candidate set (CT disabled) so idempotency is asserted deterministically
	// rather than depending on crt.sh returning a byte-identical name list.
	var lastCount int
	for run := 1; run <= 3; run++ {
		runCfg := cfg
		if run > 1 {
			runCfg.CTURL = "http://127.0.0.1:1" // deterministic dictionary-only repeat
		}
		reg := recon.DefaultRegistry(runCfg, nil)
		res := recon.Run(ctx, reg, seed, scope, runCfg)
		if _, err := as.UpsertReconAssets(0, toDBRecon(res.Records)); err != nil {
			t.Fatalf("run %d persist: %v", run, err)
		}
		cur := countAssets()
		t.Logf("run=%d records=%d written=%d rejected=%d assets(source<>'')=%d providers=%+v",
			run, len(res.Records), len(toDBRecon(res.Records)), len(res.Rejected), cur, res.Providers)
		if run > 1 && cur != lastCount {
			t.Fatalf("idempotency broken: run %d asset count %d -> %d", run, lastCount, cur)
		}
		lastCount = cur
	}
	if lastCount == 0 {
		t.Fatal("no recon assets persisted")
	}

	// Source filter is what the asset page uses.
	sources, err := as.SourceCounts()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sources=%+v", sources)
	if len(sources) == 0 {
		t.Fatal("no sources recorded")
	}
	// Alive/fingerprint detail for the manual acceptance report.
	countType := func(typ string) int {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM assets WHERE type=$1 AND source <> ''`, typ).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Logf("ASSET COUNTS: subdomain=%d service=%d ip=%d root_domain=%d",
		countType("subdomain"), countType("service"), countType("ip"), countType("root_domain"))
	alive, err := as.QueryBySource("active:http", "service", 0, 0, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ALIVE SERVICES (sample %d):", len(alive))
	for _, a := range alive {
		t.Logf("  alive url=%s status=%v title=%q favicon=%q tech=%v observed=%s",
			a.URL, derefInt(a.StatusCode), a.PageTitle, a.FaviconMMH3, a.Technologies, a.ObservedAt)
	}
	rows, err := as.QueryBySource("active:ct", "subdomain", 0, 0, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("active:ct subdomains=%d sample=%v", len(rows), func() []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.Domain)
		}
		return out
	}())

	// 2) Out-of-scope provider records are rejected and never persisted.
	bad := recon.NewRegistry()
	bad.Register(e2eProvider{name: "bad", recs: []recon.Record{
		{Type: recon.TypeSubdomain, Value: "evil.example.net", Parent: seed, Source: "bad", ObservedAt: time.Now().UTC()},
	}}, true)
	res := recon.Run(ctx, bad, seed, scope, cfg)
	if len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Value, "evil.example.net") {
		t.Fatalf("out-of-scope not rejected: %+v", res.Rejected)
	}
	var evilN int
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE domain = 'evil.example.net'`).Scan(&evilN); err != nil {
		t.Fatal(err)
	}
	if evilN != 0 {
		t.Fatal("out-of-scope asset persisted")
	}
	t.Logf("rejected=%+v", res.Rejected)

	// 3) External provider with a broken key degrades (task continues).
	cfg.FOFA = recon.FOFAConfig{Enabled: true, BaseURL: httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":true,"errmsg":"invalid key"}`))
	})).URL, Email: "a@b.c", Key: "bad-key", MaxResults: 10}
	reg := recon.DefaultRegistry(cfg, nil)
	res = recon.Run(ctx, reg, seed, scope, cfg)
	degraded := false
	for _, st := range res.Providers {
		if st.Name == "fofa" && st.Error != "" {
			degraded = true
		}
	}
	if !degraded {
		t.Fatalf("broken FOFA key should degrade provider, stats=%+v", res.Providers)
	}
	if len(res.Rejected) > 0 || len(res.Records) == 0 {
		t.Fatalf("active should still work: records=%d", len(res.Records))
	}
	t.Logf("degraded providers=%+v", res.Providers)

	// 4) Concurrency 2 vs 20 both complete; the actual cap is unit-tested.
	for _, conc := range []int{2, 20} {
		c := cfg
		c.Concurrency = conc
		start := time.Now()
		res := recon.Run(ctx, recon.DefaultRegistry(c, nil), seed, scope, c)
		t.Logf("concurrency=%d records=%d rejected=%d duration=%s", conc, len(res.Records), len(res.Rejected), time.Since(start))
	}
}

func derefInt(p *int) any {
	if p == nil {
		return "—"
	}
	return *p
}
