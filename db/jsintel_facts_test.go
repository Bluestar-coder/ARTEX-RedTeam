package db

import (
	"encoding/json"
	"testing"
)

func TestUpsertExtractedFactIdempotentAndSourceUnion(t *testing.T) {
	d := openTestDB(t)
	expID, err := d.CreateExploration("m2 fact test", "test")
	if err != nil {
		t.Fatal(err)
	}
	es := d.Exploration(expID)
	p1 := map[string]any{
		"summary": "接口: GET /api/items", "extract_kind": "js_interface", "host": "h.example.com",
		"method": "GET", "path": "/api/items", "observed_at": "2026-01-01T00:00:00Z",
		"sources": []any{map[string]any{"page_url": "https://h.example.com/", "script_url": "https://h.example.com/a.js", "line": 1}},
	}
	id1, created, err := es.UpsertExtractedFact("jsif|h.example.com|GET|/api/items", p1, nil)
	if err != nil || !created {
		t.Fatalf("first upsert id=%d created=%v err=%v", id1, created, err)
	}
	p2 := map[string]any{
		"summary": "接口: GET /api/items", "extract_kind": "js_interface", "host": "h.example.com",
		"method": "GET", "path": "/api/items", "observed_at": "2026-01-02T00:00:00Z",
		"sources": []any{map[string]any{"page_url": "https://h.example.com/other", "script_url": "https://h.example.com/b.js", "line": 3}},
	}
	id2, created, err := es.UpsertExtractedFact("jsif|h.example.com|GET|/api/items", p2, nil)
	if err != nil || created || id2 != id1 {
		t.Fatalf("second upsert id=%d created=%v err=%v (want same id, no new row)", id2, created, err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND payload->>'extract_key'=$2`, expID, "jsif|h.example.com|GET|/api/items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("fact rows=%d, want 1", n)
	}
	var payload []byte
	if err := d.QueryRow(`SELECT payload FROM exploration_nodes WHERE id=$1`, id1).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := jsonUnmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	srcs, _ := p["sources"].([]any)
	if len(srcs) != 2 {
		t.Fatalf("sources=%v, want union of 2", srcs)
	}
	if p["observed_at"] != "2026-01-02T00:00:00Z" {
		t.Fatalf("observed_at=%v, want newest", p["observed_at"])
	}
}

func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}
