package server

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/agent"
	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

func TestReconPhaseRunsStepsAfterFailureAndStoresSummary(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := newTestServer(t, m, t.TempDir(), t.TempDir(), t.TempDir())
	task, err := m.CreateTask("recon phase", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	var activities []db.Activity
	deps := reconPhaseDeps{
		enabled: true, target: "127.0.0.1:3000", domain: "127.0.0.1", dynamic: true,
		runRecon: func(context.Context, *db.AssetStore, int64, string, []string) agent.PhaseResult {
			order = append(order, "recon")
			return agent.PhaseResult{Step: "run_recon", Err: errors.New("provider down")}
		},
		runStatic: func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string) agent.PhaseResult {
			order = append(order, "static")
			return agent.PhaseResult{Step: "extract_js", Data: map[string]any{"interfaces": float64(3), "targets": float64(1)}}
		},
		runDynamic: func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string) agent.PhaseResult {
			order = append(order, "dynamic")
			return agent.PhaseResult{Step: "extract_js_dynamic", Data: map[string]any{"interfaces": float64(2)}}
		},
		emit: func(_ *Task, a db.Activity) { activities = append(activities, a) },
	}
	results := s.runReconPhaseWith(context.Background(), task, deps)
	if strings.Join(order, ",") != "recon,static,dynamic" {
		t.Fatalf("step order=%v, want recon,static,dynamic even after recon failure", order)
	}
	if len(results) != 3 {
		t.Fatalf("results=%d, want 3", len(results))
	}
	summary := task.ReconSummary()
	if !strings.Contains(summary, "run_recon 失败=provider down") || !strings.Contains(summary, "interfaces=3") {
		t.Fatalf("unexpected recon summary: %s", summary)
	}
	var sawFailure bool
	for _, a := range activities {
		if a.Worker == "recon" && a.IsError && strings.Contains(a.Summary, "provider down") {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatalf("failure not recorded in activity: %+v", activities)
	}
}

func TestReconPhaseSkipsDynamicWhenDisabled(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := newTestServer(t, m, t.TempDir(), t.TempDir(), t.TempDir())
	task, err := m.CreateTask("recon phase skip", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	deps := reconPhaseDeps{
		enabled: true, target: "example.com", domain: "example.com", dynamic: false,
		runRecon: func(context.Context, *db.AssetStore, int64, string, []string) agent.PhaseResult {
			return agent.PhaseResult{Step: "run_recon"}
		},
		runStatic: func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string) agent.PhaseResult {
			return agent.PhaseResult{Step: "extract_js"}
		},
		runDynamic: func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string) agent.PhaseResult {
			called = true
			return agent.PhaseResult{Step: "extract_js_dynamic"}
		},
		emit: func(*Task, db.Activity) {},
	}
	s.runReconPhaseWith(context.Background(), task, deps)
	if called {
		t.Fatal("dynamic step ran while disabled")
	}
	if !strings.Contains(task.ReconSummary(), "extract_js_dynamic 未启用") {
		t.Fatalf("skip note missing: %s", task.ReconSummary())
	}
}

func TestReconPhaseTargetFallsBackToTaskScope(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := &Server{m: m}
	task, err := m.CreateTask("no target text", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	task.Description = "no target text"
	task.Goal = "goal"
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	if _, err := m.Assets().AddAgentScope(id, "root_domain", "scope.example.com", "test", "manual"); err != nil {
		t.Fatal(err)
	}
	target, domain, ok := s.reconPhaseTarget(task)
	if !ok || target != "example.com" || domain != "example.com" {
		t.Fatalf("target=%q domain=%q ok=%v", target, domain, ok)
	}
}

func TestReconPhaseEnabledDefaultOn(t *testing.T) {
	t.Setenv("ARTEX_REDTEAM_RECON_PHASE_ENABLED", "")
	if !reconPhaseEnabled() {
		t.Fatal("phase must default on")
	}
	t.Setenv("ARTEX_REDTEAM_RECON_PHASE_ENABLED", "0")
	if reconPhaseEnabled() {
		t.Fatal("phase should be disabled by 0")
	}
}
