// Package testdb is the single entry point for tests that touch the PostgreSQL
// test database.
//
// Two layers protect the suite from itself:
//
//  1. A per-package schema. Lock creates `test_<pkg>_<pid>`, opens every handle
//     with `search_path=<schema>,public` (by overriding the DSN env for the
//     whole test process), and drops the schema when the package finishes. Each
//     package therefore migrates and writes its own relations — one package's
//     schema DDL can no longer touch another package's tables or rows.
//  2. A suite lock (PostgreSQL advisory lock), held for the whole package run.
//     It serializes schema migrations across packages. It is kept as defense in
//     depth: even a package that somehow skipped the lock only sees its own
//     schema, so it cannot deadlock anyone else.
//
// The lock matters because db.Open re-runs the whole schema migration on every
// call and that migration takes ACCESS EXCLUSIVE locks. Before isolation, a
// migration running outside the lock deadlocked against another package's asset
// DML (40P01), with the failure landing in whichever transaction lost the race.
//
// Tests must take the lock in TestMain (testdb.Lock) and open handles through
// testdb.Open; Open refuses to run without the lock, which makes "open before
// lock" impossible to write by accident. The lock is per package rather than per
// test because production helpers opened inside tests (Manager, stores) call
// db.Open themselves, so a per-test lease could not cover them.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/config"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// suiteLockKey serializes DB test suites across packages. All packages that
// open the test database use this same key.
const suiteLockKey = 7337741002

var (
	stateMu sync.Mutex
	locked  bool
)

// DSN returns the configured PostgreSQL DSN, or "" when none is configured.
// Once Lock has run it returns the schema-isolated DSN for this test process.
// It accepts ARTEX_REDTEAM_PG_DSN (legacy ARTEX_PG_DSN) and config.json, the
// same resolution production uses.
func DSN() string {
	dsn, _, err := config.PostgresDSN()
	if err != nil {
		return ""
	}
	return dsn
}

// Lock runs m in a per-package schema with the shared suite lock held, then
// drops the schema. Call it from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testdb.Lock(m)) }
//
// Without a configured DSN it simply runs m; DB-backed tests skip themselves in
// Open, so a DB-less environment behaves as before.
func Lock(m *testing.M) int {
	baseDSN := DSN()
	if baseDSN == "" {
		return m.Run()
	}

	schema := schemaName()
	isoDSN, err := isolatedDSN(baseDSN, schema)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[testdb] build isolated DSN failed: %v\n", err)
		return m.Run()
	}

	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[testdb] open admin connection failed: %v\n", err)
		return m.Run()
	}
	defer admin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err = admin.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[testdb] create schema %s failed: %v\n", schema, err)
		return m.Run()
	}

	// Every later open in this process — db.Open through the helper, Manager's
	// own store, raw sql.Open(db.DSN()) — must resolve to the isolated schema.
	// The env override is what makes that true for production code we can't
	// change. Keep the legacy name in sync for callers that read it directly.
	os.Setenv("ARTEX_REDTEAM_PG_DSN", isoDSN)
	os.Setenv("ARTEX_PG_DSN", isoDSN)

	lease, err := acquire(isoDSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[testdb] acquire suite lock failed: %v\n", err)
		cleanupSchema(admin, schema)
		return m.Run()
	}
	setLocked(true)
	code := m.Run()
	setLocked(false)
	lease.release()
	cleanupSchema(admin, schema)
	return code
}

// Open runs open(dsn) while the suite lock is held and registers cleanup to
// close the returned handle. T is any handle with Close() error (*db.DB, etc.).
//
// open is expected to be db.Open (or a wrapper around it) so the idempotent
// schema migration happens here, in this package's schema, under the lock.
// Calling Open without testdb.Lock in TestMain fails the test.
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

// schemaName is unique per test binary and process: the package name keeps
// schemas readable, the pid keeps two concurrent runs of the same package apart.
func schemaName() string {
	base := strings.TrimSuffix(filepath.Base(os.Args[0]), ".test")
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" {
		name = "pkg"
	}
	if len(name) > 20 {
		name = name[:20]
	}
	// Allowed identifier charset by construction; no quoting needed.
	return fmt.Sprintf("test_%s_%d", name, os.Getpid())
}

// isolatedDSN pins search_path to this package's schema (public stays as a
// fallback for anything the platform installs there) and tags the session with
// application_name so cleanup can terminate stragglers.
func isolatedDSN(base, schema string) (string, error) {
	if u, err := url.Parse(base); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		q := u.Query()
		q.Set("search_path", schema+",public")
		q.Set("application_name", schema)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	// key=value DSN form.
	return base + " search_path=" + schema + ",public application_name=" + schema, nil
}

// cleanupSchema terminates any session still tagged with this package's
// application_name, then drops the schema. Leaked goroutines from a test are the
// usual reason a plain DROP would block.
func cleanupSchema(admin *sql.DB, schema string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _ = admin.ExecContext(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1 AND pid <> pg_backend_pid()`,
		schema)
	if _, err := admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		fmt.Fprintf(os.Stderr, "[testdb] drop schema %s failed (left behind): %v\n", schema, err)
	}
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
