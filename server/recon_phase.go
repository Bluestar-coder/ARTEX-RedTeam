package server

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/agent"
	"github.com/Bluestar-coder/ARTEX-RedTeam/config"
	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
)

// reconPhaseDeps isolates the deterministic pre-planner phase for unit tests.
// Production wires the agent phase functions and the real activity emitter.
type reconPhaseDeps struct {
	enabled  bool
	target   string // host:port for run_recon
	domain   string // host without port for extract_js/dynamic
	skipNote string
	dynamic  bool

	runRecon   func(context.Context, *db.AssetStore, int64, string, []string) agent.PhaseResult
	runStatic  func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string) agent.PhaseResult
	runDynamic func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string) agent.PhaseResult
	emit       func(*Task, db.Activity)
}

func reconPhaseEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(config.Env("RECON_PHASE_ENABLED")))
	if v == "" {
		return true
	}
	switch v {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// defaultReconPhaseDeps wires the real, non-LLM phase steps. The phase is
// deliberately synchronous: startTaskEngine calls it before createGoals/engine.Run,
// so planner's first graph_overview already contains interfaces.
func (s *Server) defaultReconPhaseDeps(t *Task) reconPhaseDeps {
	as := s.m.Assets()
	var taskID int64
	if t != nil {
		taskID, _ = strconv.ParseInt(t.ID, 10, 64)
	}
	deps := reconPhaseDeps{
		enabled:  reconPhaseEnabled(),
		runRecon: agent.RunReconPhase,
		runStatic: func(ctx context.Context, as *db.AssetStore, ts *db.ExplorationStore, id int64, domain string) agent.PhaseResult {
			return agent.RunStaticPhase(ctx, as, ts, id, domain)
		},
		runDynamic: agent.RunDynamicPhase,
		emit: func(t *Task, a db.Activity) {
			if s.engine != nil && t != nil {
				s.engine.emitActivity(t, a)
			}
		},
	}
	if !deps.enabled || as == nil || t == nil || taskID <= 0 {
		return deps
	}
	target, domain, ok := s.reconPhaseTarget(t)
	if !ok {
		deps.skipNote = "未能从任务描述/目标或 task_scope 解析出目标"
		return deps
	}
	deps.target, deps.domain = target, domain
	deps.dynamic = recon.DynamicConfigFromEnv().Enabled
	return deps
}

// runReconPrephase is the single pre-planner phase entrypoint. Every failure is
// recorded and the next step still runs; it never returns before finishing all
// configured steps (unless the task context is cancelled).
func (s *Server) runReconPrephase(ctx context.Context, t *Task) {
	if t == nil {
		return
	}
	deps := s.defaultReconPhaseDeps(t)
	s.runReconPhaseWith(ctx, t, deps)
}

func (s *Server) runReconPhaseWith(ctx context.Context, t *Task, deps reconPhaseDeps) []agent.PhaseResult {
	if t == nil {
		return nil
	}
	emit := deps.emit
	if emit == nil {
		emit = func(*Task, db.Activity) {}
	}
	if !deps.enabled {
		summary := "侦察阶段：已跳过（RECON_PHASE_ENABLED=0）"
		t.SetReconSummary(summary)
		emit(t, db.Activity{Worker: "recon", Kind: "text", Summary: summary})
		return nil
	}
	if deps.target == "" || deps.domain == "" {
		summary := "侦察阶段：已跳过"
		if deps.skipNote != "" {
			summary += "（" + deps.skipNote + "）"
		}
		t.SetReconSummary(summary)
		emit(t, db.Activity{Worker: "recon", Kind: "text", Summary: summary})
		return nil
	}
	as := s.m.Assets()
	ts := t.Store
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	if as == nil || ts == nil || taskID <= 0 {
		summary := "侦察阶段：已跳过（AssetStore/ExplorationStore 未初始化）"
		t.SetReconSummary(summary)
		emit(t, db.Activity{Worker: "recon", Kind: "text", Summary: summary})
		return nil
	}

	emit(t, db.Activity{Worker: "recon", Kind: "text", Tool: "phase",
		Summary: fmt.Sprintf("侦察阶段开始：%s（确定性，不消耗 LLM）", deps.target)})

	var results []agent.PhaseResult
	runStep := func(step string, fn func() agent.PhaseResult) {
		if ctx.Err() != nil {
			return
		}
		r := fn()
		r.Step = step
		results = append(results, r)
		detail := phaseStepSummary(r)
		emit(t, db.Activity{
			Worker: "recon", Kind: "text", Tool: r.Step, IsError: r.Err != nil,
			Summary: detail,
		})
	}

	runStep("run_recon", func() agent.PhaseResult {
		return deps.runRecon(ctx, as, taskID, deps.target, nil)
	})
	runStep("extract_js", func() agent.PhaseResult {
		return deps.runStatic(ctx, as, ts, taskID, deps.domain)
	})
	if deps.dynamic {
		runStep("extract_js_dynamic", func() agent.PhaseResult {
			return deps.runDynamic(ctx, as, ts, taskID, deps.domain)
		})
	} else {
		results = append(results, agent.PhaseResult{Step: "extract_js_dynamic", Warning: "未启用（RECON_DYNAMIC_ENABLED 关闭）"})
		emit(t, db.Activity{Worker: "recon", Kind: "text", Tool: "extract_js_dynamic",
			Summary: "侦察阶段：extract_js_dynamic 跳过（未启用，默认关闭）"})
	}

	summary := phaseSummary(results)
	if ctx.Err() != nil {
		summary += "；任务上下文已取消"
	}
	t.SetReconSummary(summary)
	emit(t, db.Activity{Worker: "recon", Kind: "result", Summary: summary})
	return results
}

func phaseStepSummary(r agent.PhaseResult) string {
	var b strings.Builder
	b.WriteString("侦察阶段：")
	b.WriteString(r.Step)
	if r.Err != nil {
		b.WriteString(" 失败：")
		b.WriteString(r.Err.Error())
	} else {
		b.WriteString(" 完成")
	}
	if r.Duration > 0 {
		b.WriteString(fmt.Sprintf("（%s）", r.Duration.Round(time.Millisecond)))
	}
	if r.Data != nil {
		for _, k := range []string{"records", "written", "targets", "interfaces", "sensitives", "facts_created", "facts_updated", "pages", "xhr_fetch"} {
			if v, ok := r.Data[k]; ok {
				b.WriteString(fmt.Sprintf(" %s=%v", k, v))
			}
		}
	}
	if r.Warning != "" {
		b.WriteString("；")
		b.WriteString(r.Warning)
	}
	return b.String()
}

func phaseSummary(results []agent.PhaseResult) string {
	var b strings.Builder
	b.WriteString("侦察阶段完成：")
	failures := 0
	for i, r := range results {
		if i > 0 {
			b.WriteString("；")
		}
		b.WriteString(r.Step)
		if r.Err != nil {
			failures++
			b.WriteString(" 失败=")
			b.WriteString(r.Err.Error())
		} else if r.Data == nil && strings.Contains(r.Warning, "未启用") {
			b.WriteString(" 未启用")
		} else {
			b.WriteString(" OK")
			if n, ok := r.Data["interfaces"]; ok {
				b.WriteString(fmt.Sprintf(" interfaces=%v", n))
			}
			if n, ok := r.Data["written"]; ok {
				b.WriteString(fmt.Sprintf(" written=%v", n))
			}
		}
		if r.Warning != "" {
			b.WriteString("（" + r.Warning + "）")
		}
	}
	b.WriteString(fmt.Sprintf("；失败步骤=%d", failures))
	return b.String()
}

// reconPhaseTarget resolves the deterministic phase target from the task text
// first (the same parse the seed uses), then falls back to task_scope. It returns
// host:port for recon and the bare host for JS extraction.
func (s *Server) reconPhaseTarget(t *Task) (target, domain string, ok bool) {
	if t == nil {
		return "", "", false
	}
	scheme, host, port, parsed := parseTarget(t.Description + " " + t.Goal)
	_ = scheme
	if parsed {
		domain = host
		target = host
		if port > 0 {
			target = net.JoinHostPort(host, strconv.Itoa(port))
		}
		return target, domain, true
	}
	if s.m == nil || s.m.Assets() == nil {
		return "", "", false
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || taskID <= 0 {
		return "", "", false
	}
	scopes, err := s.m.Assets().ListTaskScope(taskID)
	if err != nil {
		return "", "", false
	}
	for _, sc := range scopes {
		switch sc.Kind {
		case "root_domain", "subdomain":
			if d := strings.TrimSpace(sc.Domain); d != "" {
				return d, d, true
			}
		case "ip", "cidr":
			raw := strings.TrimSpace(sc.Net)
			if raw == "" {
				continue
			}
			if _, cidr, err := net.ParseCIDR(raw); err == nil {
				raw = cidr.IP.String()
			}
			if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
				return ip.String(), ip.String(), true
			}
		}
	}
	return "", "", false
}
