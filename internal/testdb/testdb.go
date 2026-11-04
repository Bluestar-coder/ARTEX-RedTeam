// Package testdb is the single entry point for tests that touch the shared
// PostgreSQL test database.
//
// Every DB-touching test package must take the shared suite lock in TestMain
// (testdb.Lock) and open handles through testdb.Open. The lock exists because
// db.Open re-runs the whole schema migration on every call, and that migration
// takes ACCESS EXCLUSIVE locks; running it concurrently with another package's
// asset DML deadlocks Postgres (40P01), with the failure landing in whichever
// transaction loses the race — typically an unrelated test in another package.
//
// The lock is held for the whole package run rather than per test: production
// helpers opened inside tests (Manager, stores) call db.Open themselves, so a
// per-test lease could not cover them. testdb.Open refuses to run without the
// suite lock, which makes "open before lock" impossible to write by accident.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/config"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// suiteLockKey serializes DB test suites across packages. All packages that
// open the shared test database use this same key.
const suiteLockKey = 7337741002

var (
	stateMu sync.Mutex
	locked  bool
)

// DSN returns the configured PostgreSQL DSN, or "" when none is configured.
// It accepts ARTEX_REDTEAM_PG_DSN (legacy ARTEX_PG_DSN) and config.json, the
// same resolution production uses.
func DSN() string {
	dsn, _, err := config.PostgresDSN()
	if err != nil {
		return ""
	}
	return dsn
}

// Lock runs m with the shared suite lock held and releases it when m returns.
// Call it from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testdb.Lock(m)) }
//
// Without a configured DSN it simply runs m; DB-backed tests skip themselves in
// Open, so a DB-less environment behaves as before.
func Lock(m *testing.M) int {
	dsn := DSN()
	if dsn == "" {
		return m.Run()
	}
	lease, err := acquire(dsn)
	if err != nil {
		// Can't take the lock: run anyway so DB-less tests still pass, but say
		// loudly why DB tests may fail/skip.
		fmt.Fprintf(os.Stderr, "[testdb] acquire suite lock failed: %v\n", err)
		return m.Run()
	}
	setLocked(true)
	code := m.Run()
	setLocked(false)
	lease.release()
	return code
}

// Open runs open(dsn) while the suite lock is held and registers cleanup to
// close the returned handle. T is any handle with Close() error (*db.DB, etc.).
//
// open is expected to be db.Open (or a wrapper around it) so the idempotent
// schema migration happens here, under the lock. Calling Open without
// testdb.Lock in TestMain fails the test: that is the whole point of the
// helper.
func Open[T interface{ Close() error }](t *testing.T, open func(dsn string) (T, error)) T {
	t.Helper()
	dsn := DSN()
	if dsn == "" {
		t.Skip("no PostgreSQL DSN configured (ARTEX_REDTEAM_PG_DSN / ARTEX_PG_DSN / config.json) — skipping DB test")
	}
	stateMu.Lock()
	held := locked
	stateMu.Unlock()
	if !held {
		t.Fatalf("testdb.Open called without the suite lock: add `func TestMain(m *testing.M) { os.Exit(testdb.Lock(m)) }` to this package. " +
			"Opening the schema outside the lock deadlocks other suites (40P01).")
	}
	v, err := open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func setLocked(v bool) {
	stateMu.Lock()
	locked = v
	stateMu.Unlock()
}

type lease struct {
	pool *sql.DB
	conn *sql.Conn
}

// acquire pins the advisory lock to a dedicated session. Running
// pg_advisory_lock through *sql.DB is wrong: a later statement may be served by
// a different pooled session, releasing nothing and holding the wrong one.
func acquire(dsn string) (*lease, error) {
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Conn(context.Background())
	if err != nil {
		pool.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, suiteLockKey); err != nil {
		conn.Close()
		pool.Close()
		return nil, err
	}
	return &lease{pool: pool, conn: conn}, nil
}

func (l *lease) release() {
	_, _ = l.conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, suiteLockKey)
	_ = l.conn.Close()
	_ = l.pool.Close()
}
