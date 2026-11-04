package llmrec

import (
	"database/sql"
	"os"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestMain takes the same PostgreSQL advisory lock (7337741002) as the db,
// agent, server and evidence suites. Those packages serialize on it because
// they share one test database; llmrec was the only DB-opening package that did
// not, and db.Open re-runs the entire schema migration (ACCESS EXCLUSIVE DDL).
// The result was a real race: llmrec's DDL could deadlock with a concurrent
// suite's asset writes, failing unrelated db tests with
// "deadlock detected (40P01)" or "current transaction is aborted (25P02)"
// roughly one full `go test ./...` run in two.
func TestMain(m *testing.M) {
	dsn, _, err := db.DSN()
	if err != nil {
		os.Exit(m.Run())
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil || conn.Ping() != nil {
		os.Exit(m.Run())
	}
	defer conn.Close()
	if _, err := conn.Exec(`SELECT pg_advisory_lock(7337741002)`); err != nil {
		os.Exit(m.Run())
	}
	defer conn.Exec(`SELECT pg_advisory_unlock(7337741002)`) //nolint:errcheck
	os.Exit(m.Run())
}
