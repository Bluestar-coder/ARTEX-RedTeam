package agent

import (
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
)

func TestDefaultDynamicPageURLUsesObservedServiceScheme(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	status := 200
	if _, err := d.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "http://juice.localhost", StatusCode: &status}); err != nil {
		t.Fatal(err)
	}
	tsx := NewToolSet(nil, "planner")
	tsx.SetAssetStore(d.Assets(), d.Companies())
	sc, err := recon.NewScope("juice.localhost", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := tsx.defaultDynamicPageURL(sc); got != "http://juice.localhost/" {
		t.Fatalf("default page=%q, want observed http service", got)
	}

	ipScope, err := recon.NewScope("p1-fallback.invalid:3000", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := tsx.defaultDynamicPageURL(ipScope); got != "http://p1-fallback.invalid:3000/" {
		t.Fatalf("default page=%q, want explicit host:port", got)
	}
}
