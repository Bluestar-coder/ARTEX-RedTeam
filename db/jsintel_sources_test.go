package db

import (
	"encoding/json"
	"testing"
)

func TestMergeFactPayloadFlattensTypedSources(t *testing.T) {
	dst := map[string]any{
		"source_count": float64(1),
		"sources": []any{
			map[string]any{"source": "dynamic", "page_url": "https://x/", "snippet": "dyn"},
		},
	}
	type srcRef struct {
		Source  string `json:"source"`
		PageURL string `json:"page_url"`
		Snippet string `json:"snippet"`
	}
	src := map[string]any{
		"source_count": float64(1),
		"sources": []srcRef{
			{Source: "static", PageURL: "https://x/", Snippet: "static"},
			// The same dynamic ref already present; must not duplicate despite
			// struct-vs-map JSON key ordering.
			{Source: "dynamic", PageURL: "https://x/", Snippet: "dyn"},
		},
	}
	mergeFactPayload(dst, src)
	sources, ok := dst["sources"].([]any)
	if !ok || len(sources) != 2 {
		raw, _ := json.Marshal(dst["sources"])
		t.Fatalf("merged sources=%s want 2", raw)
	}
	if dst["source_count"] != 2 {
		t.Fatalf("source_count=%v want 2", dst["source_count"])
	}
}
