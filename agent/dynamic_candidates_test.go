package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

func TestDynamicKnownPagesFromAssetsAndFacts(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	expID, err := d.CreateExploration("dynamic candidates", "page candidates")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Assets().UpsertEndpoint(db.UpsertEndpointReq{URL: "https://www.cloudflare.com/login", Method: "GET"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Assets().UpsertEndpoint(db.UpsertEndpointReq{URL: "https://www.cloudflare.com/api/users", Method: "GET"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Assets().UpsertEndpoint(db.UpsertEndpointReq{URL: "https://other.example/admin", Method: "GET"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/account", "/api/items", "/static/app.js"} {
		_, _, err := d.Exploration(expID).UpsertExtractedFact("jsif|www.cloudflare.com|GET|"+path, map[string]any{
			"extract_kind": "js_interface", "host": "www.cloudflare.com", "method": "GET", "path": path,
			"extract_confidence": "high", "sources": []any{}, "observed_at": time.Now().UTC().Format(time.RFC3339),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	tsx := NewToolSet(d.Exploration(expID), "planner")
	tsx.SetAssetStore(d.Assets(), d.Companies())
	got := tsx.dynamicKnownPages("www.cloudflare.com", "")
	foundLogin, foundAccount := false, false
	for _, u := range got {
		t.Logf("candidate=%s", u)
		if u == "https://www.cloudflare.com/login" {
			foundLogin = true
		}
		if u == "https://www.cloudflare.com/account" {
			foundAccount = true
		}
		if u == "https://www.cloudflare.com/api/users" || u == "https://other.example/admin" || u == "https://www.cloudflare.com/static/app.js" {
			t.Fatalf("non-page/foreign candidate leaked: %s", u)
		}
	}
	if !foundLogin || !foundAccount {
		t.Fatalf("missing candidates: %v", got)
	}
	// Keep JSON import used for future payload assertions.
	_, _ = json.Marshal(got)
}

func TestDynamicKnownPagesIsServiceScoped(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	expID, err := d.CreateExploration("dynamic service scope", "page candidates")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{
		"http://127.0.0.1:3000/admin",
		"http://127.0.0.1:8081/admin",
		"http://127.0.0.1:3000/api/items",
	} {
		if _, err := d.Assets().UpsertEndpoint(db.UpsertEndpointReq{URL: u, Method: "GET"}); err != nil {
			t.Fatal(err)
		}
	}
	tsx := NewToolSet(d.Exploration(expID), "planner")
	tsx.SetAssetStore(d.Assets(), d.Companies())
	got := tsx.dynamicKnownPages("127.0.0.1", "http://127.0.0.1:3000/")
	if len(got) != 1 || got[0] != "http://127.0.0.1:3000/admin" {
		t.Fatalf("candidates=%v, want only :3000/admin", got)
	}
}
