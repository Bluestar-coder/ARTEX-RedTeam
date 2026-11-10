package agent

import (
	"encoding/json"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

func TestJSIntelOverviewGroupsByService(t *testing.T) {
	mk := func(host, path string) *db.Node {
		p := map[string]any{"extract_kind": "js_interface", "host": host, "method": "GET", "path": path, "extract_confidence": "high"}
		b, _ := json.Marshal(p)
		return &db.Node{Payload: b}
	}
	tsx := NewToolSet(nil, "planner")
	out := tsx.jsIntelOverview([]*db.Node{
		mk("127.0.0.1:3000", "/api/a"),
		mk("127.0.0.1:3000", "/api/b"),
		mk("127.0.0.1:8081", "/api/a"),
	})
	byService, _ := out["interfaces_by_service"].(map[string]int)
	if byService["127.0.0.1:3000"] != 2 || byService["127.0.0.1:8081"] != 1 {
		t.Fatalf("by_service=%v", out["interfaces_by_service"])
	}
	byHost, _ := out["interfaces_by_host"].(map[string]int)
	if byHost["127.0.0.1"] != 3 {
		t.Fatalf("by_host=%v", out["interfaces_by_host"])
	}
}
