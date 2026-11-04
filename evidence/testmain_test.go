package evidence

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Take the same suite lock as db / agent / server / llmrec before touching the
// database. The lock must come first: db.Open re-runs the entire schema
// migration (ACCESS EXCLUSIVE DDL), and running that outside the lock deadlocks
// against another suite's asset writes — the failure surfaces in the *other*
// package as "deadlock detected (40P01)".
func TestMain(m *testing.M) {
	if testPGDSN() == "" {
		os.Exit(m.Run())
	}
	os.Exit(runEvidenceSuite(m))
}
func runEvidenceSuite(m *testing.M) int {
	// Plain connection first (no schema work), just to hold the suite lock on
	// one pinned session for the whole run.
	lockDB, err := sql.Open("pgx", testPGDSN())
	if err != nil {
		panic(err)
	}
	defer lockDB.Close()
	lockConn, err := lockDB.Conn(context.Background())
	if err != nil {
		panic(err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), `SELECT pg_advisory_lock(7337741002)`); err != nil {
		panic(err)
	}
	defer lockConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7337741002)`)

	// Only now open the schema-backed handle; Open applies the migration.
	pg, err := db.Open(testPGDSN())
	if err != nil {
		panic(err)
	}
	defer pg.Close()
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
