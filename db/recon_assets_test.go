package db

import (
	"testing"
	"time"
)

// TestUpsertReconAssetsIdempotent covers the M1 persistence contract:
// same asset observed again only updates last_seen/observed_at, never adds a row,
// and source/observed_at are queryable for the asset page filter.
func TestUpsertReconAssetsIdempotent(t *testing.T) {
	d := openTestDB(t)
	as := d.Assets()
	taskID := int64(0)
	now := time.Now().UTC().Truncate(time.Second)
	status := 200
	length := int64(1234)
	records := []ReconAsset{
		{Type: "root_domain", Value: "recon-idem.example", Parent: "recon-idem.example", Source: "active:ct", ObservedAt: now},
		{Type: "subdomain", Value: "www.recon-idem.example", Parent: "recon-idem.example", Source: "active:ct", ObservedAt: now},
		{Type: "service", Value: "http://www.recon-idem.example", Parent: "recon-idem.example", Source: "active:http",
			ObservedAt: now, StatusCode: &status, ContentLength: &length, Title: "Recon", FaviconMMH3: "123", Technologies: []string{"nginx"}},
		{Type: "ip", Value: "203.0.113.77", Parent: "www.recon-idem.example", Source: "active:dns", ObservedAt: now},
		{Type: "subdomain", Value: "api.recon-idem.example", Parent: "recon-idem.example", Source: "active:ct", ObservedAt: now},
	}
	if _, err := as.UpsertReconAssets(taskID, records); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	countAssets := func() int {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM assets WHERE domain LIKE '%recon-idem%' OR ip = '203.0.113.77'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := countAssets()
	if before < 5 {
		t.Fatalf("assets=%d, want >=4", before)
	}

	// Same records, later observation, different/empty source on one record.
	records[1].ObservedAt = now.Add(time.Hour)
	records[1].Source = "active:dict"
	records[4].ObservedAt = now.Add(time.Hour)
	records[4].Source = "active:dict"
	if _, err := as.UpsertReconAssets(taskID, records); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if after := countAssets(); after != before {
		t.Fatalf("idempotency broken: assets %d -> %d", before, after)
	}

	// source filter queries
	n, err := as.CountBySource("active:ct", "subdomain", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		// both subdomains are last observed by dict/http, so active:ct has none.
		t.Fatalf("CountBySource(active:ct, subdomain)=%d, want 0", n)
	}
	n, err = as.CountBySource("active:dict", "subdomain", 0, 0)
	if err != nil || n != 1 {
		t.Fatalf("CountBySource(active:dict)=%d err=%v", n, err)
	}
	rows, err := as.QueryBySource("active:http", "service", 0, 0, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Source != "active:http" || rows[0].ObservedAt == "" || rows[0].PageTitle != "Recon" {
		t.Fatalf("service row=%+v", rows)
	}
	counts, err := as.SourceCounts()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, c := range counts {
		seen[c.Source] = c.Count
	}
	if seen["active:dns"] < 1 || seen["active:ct"] < 1 {
		t.Fatalf("SourceCounts=%+v", counts)
	}
}
