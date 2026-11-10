package server

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
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
		enabled: true,
		targets: []reconPhaseTarget{{Target: "127.0.0.1:3000", Domain: "127.0.0.1", TargetURL: "http://127.0.0.1:3000/", Label: "127.0.0.1:3000"}},
		dynamic: true,
		runRecon: func(context.Context, *db.AssetStore, int64, string, []string) agent.PhaseResult {
			order = append(order, "recon")
			return agent.PhaseResult{Step: "run_recon", Err: errors.New("provider down")}
		},
		runStatic: func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string, string) agent.PhaseResult {
			order = append(order, "static")
			return agent.PhaseResult{Step: "extract_js", Data: map[string]any{"interfaces": float64(3), "targets": float64(1)}}
		},
		runDynamic: func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string, string) agent.PhaseResult {
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
		enabled: true,
		targets: []reconPhaseTarget{{Target: "example.com:443", Domain: "example.com", TargetURL: "https://example.com/", Label: "example.com:443"}},
		dynamic: false,
		runRecon: func(context.Context, *db.AssetStore, int64, string, []string) agent.PhaseResult {
			return agent.PhaseResult{Step: "run_recon"}
		},
		runStatic: func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string, string) agent.PhaseResult {
			return agent.PhaseResult{Step: "extract_js"}
		},
		runDynamic: func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string, string) agent.PhaseResult {
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

func TestReconPhaseTargetsParsesAllSeeds(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := &Server{m: m}
	task, err := m.CreateTask("multi target", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	task.Description = "目标：http://127.0.0.1:3000/ 和 http://127.0.0.1:8081/ 以及 http://127.0.0.1:8082/"
	task.Goal = "分别评估"
	targets, note := s.reconPhaseTargets(task)
	if len(targets) != 3 {
		t.Fatalf("targets=%+v note=%q, want 3", targets, note)
	}
	seen := map[string]bool{}
	for _, tg := range targets {
		seen[tg.Target] = true
	}
	for _, want := range []string{"127.0.0.1:3000", "127.0.0.1:8081", "127.0.0.1:8082"} {
		if !seen[want] {
			t.Fatalf("missing target %s in %+v", want, targets)
		}
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

func TestReconPhaseProcessesEveryTargetIndependently(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := newTestServer(t, m, t.TempDir(), t.TempDir(), t.TempDir())
	task, err := m.CreateTask("multi phase", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	deps := reconPhaseDeps{
		enabled: true, concurrency: 1,
		targets: []reconPhaseTarget{
			{Target: "127.0.0.1:3000", Domain: "127.0.0.1", TargetURL: "http://127.0.0.1:3000/", Label: "127.0.0.1:3000"},
			{Target: "127.0.0.1:8081", Domain: "127.0.0.1", TargetURL: "http://127.0.0.1:8081/", Label: "127.0.0.1:8081"},
			{Target: "127.0.0.1:8082", Domain: "127.0.0.1", TargetURL: "http://127.0.0.1:8082/", Label: "127.0.0.1:8082"},
		},
		runRecon: func(_ context.Context, _ *db.AssetStore, _ int64, target string, _ []string) agent.PhaseResult {
			if target == "127.0.0.1:3000" {
				return agent.PhaseResult{Step: "run_recon", Err: errors.New("target down")}
			}
			return agent.PhaseResult{Step: "run_recon", Data: map[string]any{"written": float64(2)}}
		},
		runStatic: func(_ context.Context, _ *db.AssetStore, _ *db.ExplorationStore, _ int64, _ string, targetURL string) agent.PhaseResult {
			mu.Lock()
			seen[targetURL] = true
			mu.Unlock()
			return agent.PhaseResult{Step: "extract_js", Data: map[string]any{"interfaces": float64(3)}}
		},
		runDynamic: func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string, string) agent.PhaseResult {
			return agent.PhaseResult{Step: "extract_js_dynamic"}
		},
		emit: func(*Task, db.Activity) {},
	}
	s.runReconPhaseWith(context.Background(), task, deps)
	if !seen["http://127.0.0.1:3000/"] || !seen["http://127.0.0.1:8081/"] || !seen["http://127.0.0.1:8082/"] || len(seen) != 3 {
		t.Fatalf("static targets=%v, want three services", seen)
	}
	summary := task.ReconSummary()
	if !strings.Contains(summary, "127.0.0.1:3000") || !strings.Contains(summary, "127.0.0.1:8081") ||
		!strings.Contains(summary, "127.0.0.1:8082") || !strings.Contains(summary, "target down") {
		t.Fatalf("summary missing per-target data: %s", summary)
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
