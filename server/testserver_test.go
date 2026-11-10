package server

import (
	"context"
	"testing"
)

// Test resource lifecycle standard (option B: manual t.Cleanup)
//
// Server tests create many managers and only some use this helper; changing the
// helper interface to own *Manager would require touching every call site. The
// package standard is therefore:
//
//	m, err := NewManager(...)
//	if err != nil { ... }
//	t.Cleanup(func() { _ = m.Close() }) // immediately after NewManager
//	s := newTestServer(t, m, ...)        // registers its own cleanup later
//
// Never write `defer m.Close()`: defers in the test body run BEFORE t.Cleanup,
// so a t.Cleanup cleanup that touches PostgreSQL would see a closed pool and
// silently leak state into the package schema. t.Cleanup is LIFO: this helper's
// cleanup (registered after the manager close) stops the server/engine first;
// the manager close then runs last, after all DB state cleanups registered later
// in the test. Keep DB state cleanups as t.Cleanup (or defer) that do not depend
// on the background loops having stopped; if one does, register it before this
// helper.

// newTestServer builds a Server for a test and guarantees its background
// goroutines are stopped before the test's TempDir cleanup runs.
//
// New starts long-lived loops (scheduler, notifier, archive worker) and, through
// restoreTaskRuntimes, engine loops for every non-terminal task already in the
// shared test database. With a plain context.Background those loops outlive the
// test and keep touching the TempDir while testing removes it, which surfaces as
// "TempDir RemoveAll cleanup: directory not empty" — a race that only shows up
// when the whole suite runs against one database.
func newTestServer(t *testing.T, m *Manager, skillDir, dataDir, keyDir string) *Server {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, m, skillDir, dataDir, keyDir)
	t.Cleanup(func() {
		cancel()
		// StopTask cancels each task runtime and waits on its WaitGroup, so the
		// engine has genuinely stopped writing when cleanup continues.
		for _, task := range m.List() {
			s.engine.StopTask(task.ID)
		}
	})
	return s
}
