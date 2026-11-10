package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

// TestListAssetsSourceFilterAndSources covers the asset-page API contract added
// for M1: filter by recon source and expose source/observed_at on rows.
func TestListAssetsSourceFilterAndSources(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	stamp := time.Now().UnixNano()
	parent := fmt.Sprintf("src-api-%d.example.com", stamp)
	sub := "www." + parent
	// The shared package schema survives -count repeats; remove this test's
	// assets so the next iteration sees only its own row.
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM assets WHERE domain IN ($1,$2)`, parent, sub)
	})
	now := time.Now().UTC().Truncate(time.Second)
	_, err = m.pg.Assets().UpsertReconAssets(0, []db.ReconAsset{
		{Type: "subdomain", Value: sub, Parent: parent, Source: "active:ct", ObservedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m}

	req := httptest.NewRequest(http.MethodGet, "/api/assets?type=subdomain&source=active:ct", nil)
	rec := httptest.NewRecorder()
	s.listAssets(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Total  int         `json:"total"`
		Assets []*db.Asset `json:"assets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 1 || len(payload.Assets) != 1 {
		t.Fatalf("total=%d assets=%d body=%s", payload.Total, len(payload.Assets), rec.Body.String())
	}
	a := payload.Assets[0]
	if a.Domain != sub || a.Source != "active:ct" || a.ObservedAt == "" {
		t.Fatalf("row source/observed_at missing: %+v", a)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/assets/sources", nil)
	rec = httptest.NewRecorder()
	s.assetSources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sources status=%d body=%s", rec.Code, rec.Body.String())
	}
	var src struct {
		Sources []db.SourceCount `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sc := range src.Sources {
		if sc.Source == "active:ct" && sc.Count >= 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("sources=%+v", src.Sources)
	}
}
