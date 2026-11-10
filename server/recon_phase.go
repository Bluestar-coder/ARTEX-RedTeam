package server

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/agent"
	"github.com/Bluestar-coder/ARTEX-RedTeam/config"
	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
)

// reconPhaseTarget is one network target discovered from the task seed text or
// task_scope. Static/dynamic extraction are scoped to TargetURL, not just the
// host, so multiple services on one IP never share interface facts.
type reconPhaseTarget struct {
	Target    string // host:port for run_recon
	Domain    string // bare host for scope
	TargetURL string // scheme://host[:port]/ for static/dynamic
	Label     string
}

// reconPhaseDeps isolates the deterministic pre-planner phase for unit tests.
type reconPhaseDeps struct {
	enabled     bool
	targets     []reconPhaseTarget
	skipNote    string
	concurrency int
	timeout     time.Duration
	dynamic     bool

	runRecon   func(context.Context, *db.AssetStore, int64, string, []string) agent.PhaseResult
	runStatic  func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string, string) agent.PhaseResult
	runDynamic func(context.Context, *db.AssetStore, *db.ExplorationStore, int64, string, string) agent.PhaseResult
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

func reconPhaseConcurrency() int {
	n, err := strconv.Atoi(config.Env("RECON_PHASE_CONCURRENCY"))
	if err != nil || n <= 0 {
		return 2
	}
	if n > 8 {
		return 8
	}
	return n
}

func reconPhaseTimeout() time.Duration {
	n, err := strconv.Atoi(config.Env("RECON_PHASE_TIMEOUT_SEC"))
	if err != nil || n <= 0 {
		return 240 * time.Second
	}
	return time.Duration(n) * time.Second
}

func (s *Server) defaultReconPhaseDeps(t *Task) reconPhaseDeps {
	as := s.m.Assets()
	var taskID int64
	if t != nil {
		taskID, _ = strconv.ParseInt(t.ID, 10, 64)
	}
	deps := reconPhaseDeps{
		enabled:     reconPhaseEnabled(),
		concurrency: reconPhaseConcurrency(),
		timeout:     reconPhaseTimeout(),
		runRecon:    agent.RunReconPhase,
		runStatic:   agent.RunStaticPhase,
		runDynamic:  agent.RunDynamicPhase,
		emit: func(t *Task, a db.Activity) {
			if s.engine != nil && t != nil {
				s.engine.emitActivity(t, a)
			}
		},
	}
	if !deps.enabled || as == nil || t == nil || taskID <= 0 {
		return deps
	}
	deps.targets, deps.skipNote = s.reconPhaseTargets(t)
	deps.dynamic = recon.DynamicConfigFromEnv().Enabled
	return deps
}

// runReconPrephase is the pre-planner phase entrypoint. Every target and every
// step degrades independently; the phase is bounded by RECON_PHASE_TIMEOUT_SEC.
func (s *Server) runReconPrephase(ctx context.Context, t *Task) {
	if t == nil {
		return
	}
	s.runReconPhaseWith(ctx, t, s.defaultReconPhaseDeps(t))
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
	if len(deps.targets) == 0 {
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

	if deps.timeout <= 0 {
		deps.timeout = 240 * time.Second
	}
	phaseCtx, cancel := context.WithTimeout(ctx, deps.timeout)
	defer cancel()
	emit(t, db.Activity{Worker: "recon", Kind: "text", Tool: "phase",
		Summary: fmt.Sprintf("侦察阶段开始：%d 个网络目标（确定性，不消耗 LLM，最多并发 %d）", len(deps.targets), deps.concurrency)})

	outcomes := make([]targetOutcome, len(deps.targets))
	runOne := func(idx int) {
		target := deps.targets[idx]
		out := targetOutcome{target: target}
		if phaseCtx.Err() != nil {
			out.results = append(out.results, agent.PhaseResult{Step: "phase", Err: fmt.Errorf("侦察阶段总超时")})
			outcomes[idx] = out
			return
		}
		emit(t, db.Activity{Worker: "recon", Kind: "text", Tool: "phase",
			Summary: fmt.Sprintf("侦察阶段目标：%s", target.Label)})
		runStep := func(step string, fn func() agent.PhaseResult) {
			if phaseCtx.Err() != nil {
				return
			}
			t.AddToolCall()
			r := fn()
			r.Step = step
			out.results = append(out.results, r)
			emit(t, db.Activity{
				Worker: "recon", Kind: "text", Tool: step, IsError: r.Err != nil,
				Summary: phaseStepSummary(target.Label, r),
			})
		}
		runStep("run_recon", func() agent.PhaseResult {
			return deps.runRecon(phaseCtx, as, taskID, target.Target, nil)
		})
		runStep("extract_js", func() agent.PhaseResult {
			return deps.runStatic(phaseCtx, as, ts, taskID, target.Domain, target.TargetURL)
		})
		if deps.dynamic {
			runStep("extract_js_dynamic", func() agent.PhaseResult {
				return deps.runDynamic(phaseCtx, as, ts, taskID, target.Domain, target.TargetURL)
			})
		} else {
			out.results = append(out.results, agent.PhaseResult{Step: "extract_js_dynamic", Warning: "未启用（RECON_DYNAMIC_ENABLED 关闭）"})
			emit(t, db.Activity{Worker: "recon", Kind: "text", Tool: "extract_js_dynamic",
				Summary: fmt.Sprintf("侦察阶段：%s extract_js_dynamic 跳过（未启用，默认关闭）", target.Label)})
		}
		outcomes[idx] = out
	}

	concurrency := deps.concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(deps.targets) {
		concurrency = len(deps.targets)
	}
	if concurrency <= 1 {
		for i := range deps.targets {
			runOne(i)
		}
	} else {
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		for i := range deps.targets {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
					runOne(i)
				case <-phaseCtx.Done():
					outcomes[i] = targetOutcome{target: deps.targets[i], results: []agent.PhaseResult{{Step: "phase", Err: fmt.Errorf("侦察阶段总超时")}}}
				}
			}(i)
		}
		wg.Wait()
	}

	var results []agent.PhaseResult
	for _, out := range outcomes {
		results = append(results, out.results...)
	}
	summary := phaseSummary(outcomesToSummary(outcomes))
	if phaseCtx.Err() != nil {
		summary += "；侦察阶段总超时"
	}
	if deps.skipNote != "" {
		summary += "；" + deps.skipNote
	}
	t.SetReconSummary(summary)
	emit(t, db.Activity{Worker: "recon", Kind: "result", Summary: summary})
	return results
}

type targetOutcome struct {
	target  reconPhaseTarget
	results []agent.PhaseResult
}

type targetSummary struct {
	label   string
	results []agent.PhaseResult
}

func outcomesToSummary(outcomes []targetOutcome) []targetSummary {
	out := make([]targetSummary, 0, len(outcomes))
	for _, o := range outcomes {
		out = append(out, targetSummary{label: o.target.Label, results: o.results})
	}
	return out
}

func phaseStepSummary(target string, r agent.PhaseResult) string {
	var b strings.Builder
	b.WriteString("侦察阶段：")
	if target != "" {
		b.WriteString(target)
		b.WriteString(" ")
	}
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

func phaseSummary(targets []targetSummary) string {
	var b strings.Builder
	b.WriteString("侦察阶段完成：")
	failures := 0
	for i, tgt := range targets {
		if i > 0 {
			b.WriteString("；")
		}
		b.WriteString(tgt.label)
		b.WriteString(": ")
		for j, r := range tgt.results {
			if j > 0 {
				b.WriteString("，")
			}
			b.WriteString(r.Step)
			switch {
			case r.Err != nil:
				failures++
				b.WriteString(" 失败=")
				b.WriteString(r.Err.Error())
			case r.Data == nil && strings.Contains(r.Warning, "未启用"):
				b.WriteString(" 未启用")
			default:
				b.WriteString(" OK")
				if n, ok := r.Data["interfaces"]; ok {
					b.WriteString(fmt.Sprintf(" interfaces=%v", n))
				}
				if n, ok := r.Data["written"]; ok {
					b.WriteString(fmt.Sprintf(" written=%v", n))
				}
			}
			if r.Warning != "" && !strings.Contains(r.Warning, "未启用") {
				b.WriteString("（" + r.Warning + "）")
			}
		}
	}
	b.WriteString(fmt.Sprintf("；失败步骤=%d", failures))
	return b.String()
}

// parseTaskTargets extracts every network target from free-form task text. It
// deliberately returns all distinct host:port values, unlike the seed helper
// which only needs the first one.
type parsedTaskTarget struct {
	scheme string
	host   string
	port   int
	pos    int
}

func parseTaskTargets(text string) []parsedTaskTarget {
	type candidate struct {
		raw string
		pos int
	}
	var cands []candidate
	for _, re := range []*regexp.Regexp{reURL, reIPPort, reDomain} {
		for _, m := range re.FindAllStringIndex(text, -1) {
			cands = append(cands, candidate{raw: text[m[0]:m[1]], pos: m[0]})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].pos < cands[j].pos })
	seen := map[string]bool{}
	var out []parsedTaskTarget
	for _, c := range cands {
		scheme, host, port, ok := parseTarget(c.raw)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s|%d", host, port)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, parsedTaskTarget{scheme: scheme, host: host, port: port, pos: c.pos})
	}
	return out
}

// reconPhaseTargets resolves all network targets from task text, then falls back
// to task_scope when the text has no parseable target. Non-network scopes are
// reported in skipNote instead of being silently ignored.
func (s *Server) reconPhaseTargets(t *Task) ([]reconPhaseTarget, string) {
	if t == nil {
		return nil, ""
	}
	parsed := parseTaskTargets(t.Description + " " + t.Goal)
	var out []reconPhaseTarget
	for _, p := range parsed {
		scheme := p.scheme
		if scheme != "http" && scheme != "https" {
			scheme = "http"
		}
		hostForURL := p.host
		if p.port > 0 && !(scheme == "https" && p.port == 443) && !(scheme == "http" && p.port == 80) {
			hostForURL = net.JoinHostPort(p.host, strconv.Itoa(p.port))
		}
		target := p.host
		if p.port > 0 {
			target = net.JoinHostPort(p.host, strconv.Itoa(p.port))
		}
		out = append(out, reconPhaseTarget{
			Target: target, Domain: p.host,
			TargetURL: scheme + "://" + hostForURL + "/",
			Label:     target,
		})
	}
	if len(out) > 0 {
		return out, ""
	}
	if s.m == nil || s.m.Assets() == nil {
		return nil, "未能解析网络目标"
	}
	taskID, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || taskID <= 0 {
		return nil, "未能解析网络目标"
	}
	scopes, err := s.m.Assets().ListTaskScope(taskID)
	if err != nil {
		return nil, "读取 task_scope 失败: " + err.Error()
	}
	var skipped []string
	for _, sc := range scopes {
		switch sc.Kind {
		case "root_domain", "subdomain":
			if d := strings.TrimSpace(sc.Domain); d != "" {
				out = append(out, reconPhaseTarget{Target: d, Domain: d, TargetURL: "https://" + d + "/", Label: d})
			}
		case "ip", "cidr":
			raw := strings.TrimSpace(sc.Net)
			if raw == "" {
				continue
			}
			if _, cidr, e := net.ParseCIDR(raw); e == nil {
				raw = cidr.IP.String()
			}
			if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
				out = append(out, reconPhaseTarget{Target: ip.String(), Domain: ip.String(), TargetURL: "http://" + ip.String() + "/", Label: ip.String()})
			}
		case "keyword", "company", "icp":
			skipped = append(skipped, sc.Kind)
		}
	}
	note := ""
	if len(skipped) > 0 {
		note = "跳过非网络 scope: " + strings.Join(skipped, ",")
	}
	if len(out) == 0 && note == "" {
		note = "未能解析网络目标（关键词/公司名目标跳过）"
	}
	return out, note
}

// reconPhaseTarget is kept as a compatibility helper for older unit tests and
// callers that need exactly one target.
func (s *Server) reconPhaseTarget(t *Task) (target, domain string, ok bool) {
	targets, _ := s.reconPhaseTargets(t)
	if len(targets) == 0 {
		return "", "", false
	}
	return targets[0].Target, targets[0].Domain, true
}
