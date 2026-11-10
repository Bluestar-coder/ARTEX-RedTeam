package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

func phaseInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func TestDeterministicReconPhaseProducesAssetsAndInterfaces(t *testing.T) {
	t.Setenv("ARTEX_REDTEAM_RECON_ENABLED", "active")
	t.Setenv("ARTEX_REDTEAM_RECON_CT_BACKENDS", "")
	t.Setenv("ARTEX_REDTEAM_RECON_WORDLIST", "")
	t.Setenv("RECON_DYNAMIC_ENABLED", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><title>Phase App</title><script src="/app.js"></script></html>`))
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`fetch("/api/phase", {method: "GET"});`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	target := strings.TrimPrefix(srv.URL, "http://")

	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("phase integration", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Assets().AddAgentScope(task.ID, "ip", "127.0.0.1", "test", "manual"); err != nil {
		t.Fatal(err)
	}

	reconRes := RunReconPhase(context.Background(), d.Assets(), task.ID, target, nil)
	if reconRes.Err != nil {
		t.Fatalf("recon phase: %v", reconRes.Err)
	}
	if n := phaseInt(reconRes.Data["written"]); n == 0 {
		t.Fatalf("recon wrote no assets: %+v", reconRes.Data)
	}
	staticRes := RunStaticPhase(context.Background(), d.Assets(), d.Exploration(task.ExplorationID), task.ID, "127.0.0.1", srv.URL)
	if staticRes.Err != nil {
		t.Fatalf("static phase: %v", staticRes.Err)
	}
	if n := phaseInt(staticRes.Data["interfaces"]); n == 0 {
		t.Fatalf("static phase extracted no interfaces: %+v", staticRes.Data)
	}
	facts, err := d.Exploration(task.ExplorationID).ListByKind(db.KindFact, 100)
	if err != nil {
		t.Fatal(err)
	}
	var jsInterfaces int
	var sawServiceHost bool
	for _, f := range facts {
		var p map[string]any
		if json.Unmarshal(f.Payload, &p) != nil || p["extract_kind"] != "js_interface" {
			continue
		}
		jsInterfaces++
		if h, _ := p["host"].(string); strings.HasPrefix(h, "127.0.0.1:") {
			sawServiceHost = true
		}
	}
	if jsInterfaces == 0 {
		t.Fatal("interface facts were not persisted")
	}
	if !sawServiceHost {
		t.Fatal("interface fact did not carry host:port target identity")
	}
}
