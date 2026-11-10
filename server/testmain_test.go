package server

import (
	"os"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/testdb"
)

// TestMain holds the shared PostgreSQL suite lock for the whole package run.
// Every DB handle must be opened through testdb.Open so the schema migration
// runs under that lock; see internal/testdb for why.
func TestMain(m *testing.M) {
	enableGoalSignalsForTests()
	// The deterministic recon pre-phase is on by default in production; the
	// package's lifecycle tests use synthetic hosts and must not touch the
	// network, so the test binary opts out. The phase itself is covered
	// directly in recon_phase_test.go and end-to-end in the local lab.
	_ = os.Setenv("ARTEX_REDTEAM_RECON_PHASE_ENABLED", "0")
	os.Exit(testdb.Lock(m))
}
