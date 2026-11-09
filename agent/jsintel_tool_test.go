package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

func TestExtractJSToolWritesFactsAndOverview(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	expID, err := d.CreateExploration("m2 jsintel test", "find apis")
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>
<script src="/app.js"></script>
<script>const appid="wx1234567890abcdef"; fetch("/api/login", {method:"POST", body: JSON.stringify({username:u, password:p})});</script>
</body></html>`))
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`
async function load(){ return fetch("/api/users?page=1", {method:"GET"}); }
axios.post("/api/upload", {file: f, token: "t"});
const secretKey = "9f8a7b6c5d4e3f201122334455667788";
const internal = "http://10.12.0.8:8080/health";
const admin = "/admin/dashboard";
`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	status := 200
	_, err = d.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: srv.URL, StatusCode: &status})
	if err != nil {
		t.Fatal(err)
	}

	tsx := NewToolSet(d.Exploration(expID), "planner")
	tsx.SetAssetStore(d.Assets(), d.Companies())

	call := func() map[string]any {
		raw, _ := json.Marshal(map[string]any{"domain": "127.0.0.1"})
		res, err := tsx.extractJS().Call(context.Background(), raw, nil)
		if err != nil {
			t.Fatalf("extract_js call: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(res.Flatten()), &out); err != nil {
			t.Fatalf("result=%s err=%v", res.Flatten(), err)
		}
		return out
	}
	first := call()
	if first["facts_created"].(float64) == 0 || first["interfaces"].(float64) == 0 {
		t.Fatalf("first run=%v", first)
	}
	createdFirst := int(first["facts_created"].(float64))
	second := call()
	if int(second["facts_created"].(float64)) != 0 || int(second["facts_updated"].(float64)) != createdFirst {
		t.Fatalf("second run should only update facts: first=%v second=%v", first, second)
	}
	var factCount int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND payload ? 'extract_key'`, expID).Scan(&factCount); err != nil {
		t.Fatal(err)
	}
	if factCount != createdFirst {
		t.Fatalf("fact rows=%d created=%d", factCount, createdFirst)
	}

	ov := tsx.graphOverviewData()
	js, ok := ov["js_intel"].(map[string]any)
	if !ok {
		t.Fatalf("graph_overview missing js_intel: %v", ov)
	}
	toInt := func(v any) int {
		switch x := v.(type) {
		case int:
			return x
		case float64:
			return int(x)
		}
		return 0
	}
	if toInt(js["total_interfaces"]) == 0 || toInt(js["sensitive_total"]) == 0 {
		t.Fatalf("js_intel=%v", js)
	}
	byConf, ok := js["by_confidence"].(map[string]int)
	if !ok || len(byConf) == 0 {
		t.Fatalf("js_intel missing by_confidence: %v", js)
	}
	top, _ := js["top_interfaces"].([]map[string]any)
	if len(top) > 0 {
		if _, has := top[0]["confidence"]; !has {
			t.Fatalf("top_interfaces missing confidence: %v", top[0])
		}
	}
	var withConf int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND payload->>'extract_kind'='js_interface' AND COALESCE(payload->>'extract_confidence','') <> ''`, expID).Scan(&withConf); err != nil {
		t.Fatal(err)
	}
	if withConf == 0 {
		t.Fatal("interface facts missing extract_confidence")
	}
	raw, _ := json.MarshalIndent(js, "", "  ")
	t.Logf("graph_overview.js_intel:\n%s", raw)
}

func TestJSIntelOverviewBySource(t *testing.T) {
	mk := func(payload map[string]any) *db.Node {
		raw, _ := json.Marshal(payload)
		return &db.Node{ID: 1, Payload: raw}
	}
	facts := []*db.Node{
		mk(map[string]any{
			"extract_kind": "js_interface", "host": "app.example", "method": "GET", "path": "/api/static",
			"extract_confidence": "high", "params": []any{}, "tags": []any{}, "source_count": 1,
			"sources": []any{map[string]any{"source": "static", "script_url": "https://app.example/a.js"}},
		}),
		mk(map[string]any{
			"extract_kind": "js_interface", "host": "app.example", "method": "POST", "path": "/api/merged",
			"extract_confidence": "high", "params": []any{"id"}, "tags": []any{}, "source_count": 2,
			"sources": []any{
				map[string]any{"source": "static", "script_url": "https://app.example/a.js"},
				map[string]any{"source": "dynamic", "script_url": "https://app.example/"},
			},
		}),
		mk(map[string]any{
			"extract_kind": "js_interface", "host": "app.example", "method": "GET", "path": "/api/legacy",
			"extract_confidence": "low", "params": []any{}, "tags": []any{}, "source_count": 1,
			"sources": []any{map[string]any{"script_url": "https://app.example/old.js"}},
		}),
	}
	ov := (&ToolSet{}).jsIntelOverview(facts)
	if ov == nil {
		t.Fatal("nil overview")
	}
	by, ok := ov["by_source"].(map[string]int)
	if !ok {
		t.Fatalf("by_source missing: %v", ov)
	}
	if by["static"] != 3 || by["dynamic"] != 1 {
		t.Fatalf("by_source=%v want static=3 dynamic=1", by)
	}
	top, _ := ov["top_interfaces"].([]map[string]any)
	foundMerged := false
	for _, row := range top {
		if row["path"] == "/api/merged" {
			foundMerged = true
			ev, _ := row["evidence_sources"].([]string)
			if len(ev) != 2 {
				t.Fatalf("merged evidence_sources=%v", ev)
			}
		}
	}
	if !foundMerged {
		t.Fatalf("merged interface not in top: %v", top)
	}
}
