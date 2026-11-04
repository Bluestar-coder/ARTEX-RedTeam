package server

import (
	"context"
	"testing"
)

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
