package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

// TestExtractJSE2E is the real-site M2 acceptance test (skipped unless
// JSINTEL_E2E_URL is set). It uses real HTTP to one front-end URL, writes facts,
// checks idempotency, scope filtering and the size cap, and prints the
// graph_overview js_intel JSON for manual review.
func TestExtractJSE2E(t *testing.T) {
	targetURL := strings.TrimSpace(os.Getenv("JSINTEL_E2E_URL"))
	if targetURL == "" {
		t.Skip("set JSINTEL_E2E_URL to run the real-site JS extraction E2E")
	}
	d := testDB(t)
	defer d.Close()
	expID, err := d.CreateExploration("m2 jsintel e2e", "extract apis from "+targetURL)
	if err != nil {
		t.Fatal(err)
	}
	status := 200
	_, err = d.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: targetURL, StatusCode: &status})
	if err != nil {
		t.Fatal(err)
	}
	// An out-of-scope alive service: it must never be fetched for a different domain.
	_, err = d.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "https://example.org/", StatusCode: &status})
	if err != nil {
		t.Fatal(err)
	}

	tsx := NewToolSet(d.Exploration(expID), "planner")
	tsx.SetAssetStore(d.Assets(), d.Companies())
	call := func(domain string) map[string]any {
		raw, _ := json.Marshal(map[string]any{"domain": domain, "limit": 30})
		res, err := tsx.extractJS().Call(context.Background(), raw, nil)
		if err != nil {
			t.Fatalf("extract_js: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(res.Flatten()), &out); err != nil {
			t.Fatalf("result=%s err=%v", res.Flatten(), err)
		}
		return out
	}

	host := strings.TrimPrefix(strings.TrimPrefix(targetURL, "https://"), "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	first := call(host)
	count := func() int {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND payload ? 'extract_key'`, expID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	afterFirst := count()
	t.Logf("first run: %v", first)
	if afterFirst == 0 || first["facts_created"].(float64) == 0 {
		t.Fatalf("no facts extracted from %s: %v", targetURL, first)
	}
	second := call(host)
	if second["facts_created"].(float64) != 0 || count() != afterFirst {
		t.Fatalf("idempotency broken: first=%v second=%v facts=%d->%d", first, second, afterFirst, count())
	}
	t.Logf("idempotency OK: facts=%d after two runs", afterFirst)

	// Detail log for manual review: every interface/sensitive fact + its snippet.
	rows, err := d.Query(`SELECT payload FROM exploration_nodes
WHERE exploration_id=$1 AND kind='fact' AND payload->>'extract_kind' IN ('js_interface','js_sensitive')
ORDER BY id`, expID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var rawPayload []byte
		if err := rows.Scan(&rawPayload); err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		_ = json.Unmarshal(rawPayload, &p)
		srcs, _ := p["sources"].([]any)
		snippet := ""
		script := ""
		if len(srcs) > 0 {
			if m, ok := srcs[0].(map[string]any); ok {
				snippet, _ = m["snippet"].(string)
				script, _ = m["script_url"].(string)
			}
		}
		t.Logf("FACT kind=%v summary=%q value=%v script=%s snippet=%q", p["extract_kind"], p["summary"], p["value"], script, strings.TrimSpace(snippet))
	}
	rows.Close()

	// Scope: example.org must be listed as skipped_scope, never fetched.
	skippedJSON, _ := json.Marshal(second["skipped_scope"])
	if !strings.Contains(string(skippedJSON), "example.org") {
		t.Fatalf("out-of-scope service not skipped: %s", skippedJSON)
	}
	t.Logf("skipped_scope=%s", skippedJSON)

	// Planner view: graph_overview.js_intel actual JSON.
	ov := tsx.graphOverviewData()
	js, ok := ov["js_intel"].(map[string]any)
	if !ok {
		t.Fatalf("graph_overview missing js_intel: %v", ov)
	}
	raw, _ := json.MarshalIndent(js, "", "  ")
	t.Logf("graph_overview.js_intel:\n%s", raw)

	// Size protection on a locally constructed oversized bundle.
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`<script src="/huge.js"></script>`))
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("var padding = 1;", 400000))) // ~6 MB
	}))
	defer big.Close()
	bigStatus := 200
	if _, err := d.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: big.URL, StatusCode: &bigStatus}); err != nil {
		t.Fatal(err)
	}
	bigRes := call("127.0.0.1")
	skippedBig, _ := json.Marshal(bigRes["skipped"])
	if !strings.Contains(string(skippedBig), "file_too_large") {
		t.Fatalf("oversized JS not skipped: %s", skippedBig)
	}
	t.Logf("size-cap skipped=%s", skippedBig)
}
