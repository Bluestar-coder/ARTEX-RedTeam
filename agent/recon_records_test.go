package agent

import (
	"testing"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
)

func TestAssetOnlyReconRecordsDropsInterfaces(t *testing.T) {
	in := []recon.Record{
		{Type: recon.TypeRootDomain, Value: "example.com", ObservedAt: time.Now()},
		{Type: recon.TypeService, Value: "http://example.com", ObservedAt: time.Now()},
		{Type: recon.TypeInterface, Value: "http://example.com/api", ObservedAt: time.Now()},
	}
	out := assetOnlyReconRecords(in)
	if len(out) != 2 {
		t.Fatalf("asset records=%d, want 2", len(out))
	}
	for _, r := range out {
		if r.Type == recon.TypeInterface {
			t.Fatalf("interface record leaked into assets: %+v", r)
		}
	}
}
