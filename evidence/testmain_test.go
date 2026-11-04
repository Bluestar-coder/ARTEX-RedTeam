package evidence

import (
	"context"
	"os"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

// Initialize an explicitly configured fresh database before taking the same
// suite lock as db, agent and server. Hold it on one pinned connection.
func TestMain(m *testing.M) {
	if testPGDSN() == "" {
		os.Exit(m.Run())
	}
	os.Exit(runEvidenceSuite(m))
}
func runEvidenceSuite(m *testing.M) int {
	pg, err := db.Open(testPGDSN())
	if err != nil {
		panic(err)
	}
	defer pg.Close()
	conn, err := pg.Conn(context.Background())
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), `SELECT pg_advisory_lock(7337741002)`); err != nil {
		panic(err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7337741002)`)
	return m.Run()
}

// testPGDSN resolves the integration-test database from the new env name with
// the pre-rename one as a fallback.
func testPGDSN() string {
	if v := os.Getenv("ARTEX_REDTEAM_PG_DSN"); v != "" {
		return v
	}
	return os.Getenv("ARTEX_PG_DSN")
}
