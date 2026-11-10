package server

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

// isolateGoalSeeding removes ambient LLM state that earlier tests in this
// package leave in the shared test schema. The core lifecycle test only checks
// that bootstrap goals exist; a leaked active/bound profile would make the async
// goal decomposer perform a real LLM call and race that assertion. The snapshot
// is restored after the server has stopped and before the DB is closed.
func isolateGoalSeeding(t *testing.T, m *Manager) {
	t.Helper()
	var activeID int64
	if p, err := m.pg.ActiveProfile(); err == nil && p != nil {
		activeID = p.ID
	}
	type binding struct {
		key string
		id  int64
	}
	var bindings []binding
	rows, err := m.pg.Query(`SELECT key, llm_profile_id FROM agents WHERE llm_profile_id IS NOT NULL`)
	if err == nil {
		for rows.Next() {
			var b binding
			if rows.Scan(&b.key, &b.id) == nil {
				bindings = append(bindings, b)
			}
		}
		rows.Close()
	} else if err != sql.ErrNoRows {
		t.Logf("snapshot agent llm bindings: %v", err)
	}
	enabled, limit := m.ConcurrencyLimit()
	if _, err := m.pg.Exec(`UPDATE llm_profiles SET is_default=false`); err != nil {
		t.Fatalf("clear active llm profile: %v", err)
	}
	if _, err := m.pg.Exec(`UPDATE agents SET llm_profile_id=NULL`); err != nil {
		t.Fatalf("clear agent llm bindings: %v", err)
	}
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatalf("reset concurrency: %v", err)
	}
	t.Cleanup(func() {
		// Runs after newTestServer's cleanup (server/engine stopped), before the
		// manager close registered earlier.
		for _, b := range bindings {
			if _, err := m.pg.Exec(`UPDATE agents SET llm_profile_id=$1 WHERE key=$2`, b.id, b.key); err != nil {
				log.Printf("[test] restore agent llm binding %s: %v", b.key, err)
			}
		}
		if activeID > 0 {
			if err := m.pg.SetActiveProfile(activeID); err != nil {
				log.Printf("[test] restore active profile: %v", err)
			}
		}
		if err := m.SetConcurrency(enabled, limit); err != nil {
			log.Printf("[test] restore concurrency: %v", err)
		}
	})
}

func waitGoalNodes(t *testing.T, s *Server, m *Manager, expID int64) []*db.Node {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.waitGoalsReady(ctx, expID); err != nil {
		t.Fatalf("wait for goal seeding: %v", err)
	}
	goals, err := m.pg.Exploration(expID).ListByKind("goal", 10)
	if err != nil {
		t.Fatal(err)
	}
	return goals
}

func TestCoreGoalSeedingIgnoresLeakedLLMProfile(t *testing.T) {
	// Diagnostic switch: run with CORE_FLAKE_NO_ISOLATION=1 to reproduce the old
	// behaviour (ambient active LLM profile -> hanging goal decomposition).
	noIsolation := os.Getenv("CORE_FLAKE_NO_ISOLATION") == "1"
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
			w.WriteHeader(http.StatusBadGateway)
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()

	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	profileID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name: "leaked-hanging", Format: "openai", BaseURL: hang.URL + "/v1", Model: "x", APIKey: "k",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetActiveProfile(profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`UPDATE agents SET llm_profile_id=$1 WHERE key='goals'`, profileID); err != nil {
		t.Fatal(err)
	}
	// Cleanup registered before newTestServer so it runs after the server stopped
	// and before the manager closes.
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM llm_profiles WHERE id=$1`, profileID)
		_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false`)
		_, _ = m.pg.Exec(`UPDATE agents SET llm_profile_id=NULL WHERE llm_profile_id=$1`, profileID)
	})
	if !noIsolation {
		if _, err := m.pg.Exec(`UPDATE llm_profiles SET is_default=false`); err != nil {
			t.Fatal(err)
		}
		if _, err := m.pg.Exec(`UPDATE agents SET llm_profile_id=NULL`); err != nil {
			t.Fatal(err)
		}
		if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
			t.Fatal(err)
		}
	}
	td := t.TempDir()
	s := newTestServer(t, m, td, td, td)
	task, err := m.CreateTask("leaked profile", "测试 SQLi/XSS", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.launchTask(task, "leaked profile", false)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.waitGoalsReady(ctx, task.ExpID); err != nil {
		t.Fatalf("goal seeding was blocked by leaked LLM profile: %v", err)
	}
	goals, err := m.pg.Exploration(task.ExpID).ListByKind("goal", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) == 0 {
		t.Fatal("expected fallback goal after isolation")
	}
	t.Logf("goals=%d", len(goals))
}
