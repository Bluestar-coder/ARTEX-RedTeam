package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

// Scheduler drives P3 triggers: on each tick it fires due interval triggers and
// scans for new findings / newly-met goals (any task) to fire event triggers.
// Each fire enqueues a NEW conversation on the agent's per-agent trigger queue
// (StartTriggeredRun): fires for the same agent run one at a time in FIFO order,
// distinct agents still run concurrently. State is persisted (per-trigger last_fire
// + finding watermark + fired-goal set) so a restart resumes without double-firing.
// Triggers only attach to CUSTOM agents.
type Scheduler struct {
	s    *Server
	pg   *db.DB
	tick time.Duration
}

const (
	schedKeyLastFinding    = "last_finding_id"    // watermark: max finding node id fired for
	schedKeyFiredGoals     = "fired_goals"        // JSON array of goal node ids already fired
	schedKeyLastTimeout    = "last_timeout_id"    // watermark: max task id fired for task timeout
	schedKeyLastToolCall   = "last_toolcall_id"   // watermark: max activity id fired for tool call
	schedKeyLastTaskCreate = "last_taskcreate_id" // watermark: max task id fired for task create
)

func newScheduler(s *Server) *Scheduler {
	return &Scheduler{s: s, pg: s.m.pg, tick: 5 * time.Second}
}

// Run loops until ctx is done, ticking the scheduler. Started once from server New.
func (sc *Scheduler) Run(ctx context.Context) {
	if sc.pg == nil {
		return
	}
	sc.init()
	t := time.NewTicker(sc.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sc.s.reconcileConcurrency() // 并发上限:有空位就把排队任务补位启动
			sc.step()
		}
	}
}

// seedWatermark 首次启动时把 watermark 初始化为当前最大事件 id，避免把历史
// backlog 一次性重放。query(0) 复用与增量触发完全相同的查询（SQL/语义不变）。
func (sc *Scheduler) seedWatermark(key string, query func(last int64) ([]db.TaskEvent, error)) {
	if sc.mustState(key) != "" {
		return
	}
	var maxID int64
	if evs, err := query(0); err == nil {
		for _, e := range evs {
			if e.NodeID > maxID {
				maxID = e.NodeID
			}
		}
	}
	_ = sc.pg.SetSchedState(key, strconv.FormatInt(maxID, 10))
}

// init seeds the watermarks on first run so pre-existing findings/goals don't all
// fire at once — only events created AFTER the scheduler first starts count.
func (sc *Scheduler) init() {
	sc.seedWatermark(schedKeyLastFinding, sc.pg.NewFindingsSince)
	sc.seedWatermark(schedKeyLastTimeout, sc.pg.TimedOutTasksSince)
	sc.seedWatermark(schedKeyLastToolCall, sc.pg.NewToolCallsSince)
	sc.seedWatermark(schedKeyLastTaskCreate, sc.pg.NewTasksSince)

	if sc.mustState(schedKeyFiredGoals) == "" {
		set := map[int64]bool{}
		if evs, err := sc.pg.MetGoals(); err == nil {
			for _, e := range evs {
				set[e.NodeID] = true
			}
		}
		sc.saveFiredGoalSet(set)
	}
}

func (sc *Scheduler) step() {
	triggers, err := sc.pg.ListEnabledTriggers()
	if err != nil {
		return
	}
	if len(triggers) == 0 {
		return
	}
	sc.fireIntervals(triggers)
	sc.fireFindings(triggers)
	sc.fireGoals(triggers)
	sc.fireTaskTimeouts(triggers)
	sc.fireToolCalls(triggers)
	sc.fireTaskCreates(triggers)
}

// fireIntervals fires triggers whose interval has elapsed since last_fire.
func (sc *Scheduler) fireIntervals(triggers []*db.AgentTrigger) {
	now := time.Now()
	for _, tr := range triggers {
		if tr.IntervalSec <= 0 {
			continue
		}
		due := tr.LastFire == nil || now.Sub(*tr.LastFire) >= time.Duration(tr.IntervalSec)*time.Second
		if !due {
			continue
		}
		_ = sc.pg.TouchTriggerFire(tr.ID)
		ctx := "\n\n【本次为定时触发】" + now.Format(" 2006-01-02 15:04:05 MST")
		sc.s.StartTriggeredRun(tr.AgentKey, fmt.Sprintf("定时触发 · %s", now.Format("15:04")), tr.IntervalMessage+ctx, 0, false, "", "")
	}
}

// watermarkFire 是一次「按持久化 watermark 增量触发」的差异描述。fireWatermark
// 统一实现控制流：读 watermark → 查增量事件（SQL 由调用方的 db 方法提供，语义
// 不变）→ 推进 watermark（即使没有活跃触发器也要推进，避免后续启用时重放历史）
// → 分发触发器。各触发器的匹配条件、跳过规则、标题与消息模板通过闭包传入。
type watermarkFire struct {
	stateKey string
	match    func(*db.AgentTrigger) bool
	query    func(last int64) ([]db.TaskEvent, error)
	skip     func(db.TaskEvent) bool
	runTitle func(db.TaskEvent) string
	ctxMsg   func(db.TaskEvent) string
	message  func(*db.AgentTrigger, string) string
	allow    func(*db.AgentTrigger, db.TaskEvent) bool
}

func (sc *Scheduler) fireWatermark(triggers []*db.AgentTrigger, cfg watermarkFire) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if cfg.match(tr) {
			want = append(want, tr)
		}
	}
	last, _ := strconv.ParseInt(sc.mustState(cfg.stateKey), 10, 64)
	events, err := cfg.query(last)
	if err != nil || len(events) == 0 {
		return
	}
	maxID := last
	for _, e := range events {
		if e.NodeID > maxID {
			maxID = e.NodeID
		}
		if len(want) == 0 {
			continue
		}
		if cfg.skip != nil && cfg.skip(e) {
			continue
		}
		msgCtx := cfg.ctxMsg(e)
		for _, tr := range want {
			if cfg.allow != nil && !cfg.allow(tr, e) {
				continue
			}
			sc.s.StartTriggeredRun(tr.AgentKey, cfg.runTitle(e), cfg.message(tr, msgCtx), e.TaskID, true, e.TaskDesc, e.TaskGoal)
		}
	}
	_ = sc.pg.SetSchedState(cfg.stateKey, strconv.FormatInt(maxID, 10))
}

// fireFindings fires on_finding triggers for findings above the persisted
// watermark (monotonic node id → no double-fire across restarts).
func (sc *Scheduler) fireFindings(triggers []*db.AgentTrigger) {
	sc.fireWatermark(triggers, watermarkFire{
		stateKey: schedKeyLastFinding,
		match:    func(tr *db.AgentTrigger) bool { return tr.OnFinding },
		query:    sc.pg.NewFindingsSince,
		runTitle: func(e db.TaskEvent) string { return fmt.Sprintf("finding 触发 · task#%d", e.TaskID) },
		ctxMsg: func(e db.TaskEvent) string {
			return fmt.Sprintf("\n\n【本次由任务发现 finding 触发】\n发现: [%s/%s] %s", e.VulnClass, e.Severity, e.Summary)
		},
		message: func(tr *db.AgentTrigger, c string) string { return tr.FindingMessage + c },
	})
}

// fireGoals fires on_goal_met triggers for met goals not yet in the fired set.
func (sc *Scheduler) fireGoals(triggers []*db.AgentTrigger) {
	var want []*db.AgentTrigger
	for _, tr := range triggers {
		if tr.OnGoalMet {
			want = append(want, tr)
		}
	}
	events, err := sc.pg.MetGoals()
	if err != nil || len(events) == 0 {
		return
	}
	// Mark goals as consumed whether or not a trigger is active, so enabling an
	// on_goal_met trigger later doesn't replay every already-met goal.
	fired := sc.firedGoalSet()
	changed := false
	for _, e := range events {
		if fired[e.NodeID] {
			continue
		}
		fired[e.NodeID] = true
		changed = true
		if len(want) == 0 {
			continue
		}
		msgCtx := fmt.Sprintf("\n\n【本次由任务完成目标触发】\n达成目标: %s", e.Summary)
		for _, tr := range want {
			sc.s.StartTriggeredRun(tr.AgentKey, fmt.Sprintf("目标触发 · task#%d", e.TaskID), tr.GoalMessage+msgCtx, e.TaskID, true, e.TaskDesc, e.TaskGoal)
		}
	}
	if changed {
		sc.saveFiredGoalSet(fired)
	}
}

// fireTaskTimeouts fires on_task_timeout triggers for tasks that newly reached
// status='timeout' above the persisted watermark (task id → no double-fire).
func (sc *Scheduler) fireTaskTimeouts(triggers []*db.AgentTrigger) {
	sc.fireWatermark(triggers, watermarkFire{
		stateKey: schedKeyLastTimeout,
		match:    func(tr *db.AgentTrigger) bool { return tr.OnTaskTimeout },
		query:    sc.pg.TimedOutTasksSince,
		runTitle: func(e db.TaskEvent) string { return fmt.Sprintf("超时触发 · task#%d", e.TaskID) },
		ctxMsg:   func(db.TaskEvent) string { return "\n\n【本次由任务超时触发】" },
		message:  func(tr *db.AgentTrigger, c string) string { return tr.TaskTimeoutMessage + c },
	})
}

// fireTaskCreates fires on_task_create triggers for tasks newly created above the
// persisted watermark (task id → no double-fire across restarts).
func (sc *Scheduler) fireTaskCreates(triggers []*db.AgentTrigger) {
	sc.fireWatermark(triggers, watermarkFire{
		stateKey: schedKeyLastTaskCreate,
		match:    func(tr *db.AgentTrigger) bool { return tr.OnTaskCreate },
		query:    sc.pg.NewTasksSince,
		runTitle: func(e db.TaskEvent) string { return fmt.Sprintf("任务创建触发 · task#%d", e.TaskID) },
		ctxMsg:   func(db.TaskEvent) string { return "\n\n【本次由任务创建触发】" },
		message:  func(tr *db.AgentTrigger, c string) string { return tr.TaskCreateMessage + c },
	})
}

// fireToolCalls fires on_tool_call triggers for tool calls (tool_result rows) above
// the persisted watermark whose tool name is in the trigger's selected set. The fire
// message carries the task id/desc/goal + tool name + (truncated) input & output.
func (sc *Scheduler) fireToolCalls(triggers []*db.AgentTrigger) {
	sc.fireWatermark(triggers, watermarkFire{
		stateKey: schedKeyLastToolCall,
		match:    func(tr *db.AgentTrigger) bool { return tr.OnToolCall && len(tr.ToolNames) > 0 },
		query:    sc.pg.NewToolCallsSince,
		// A failed finding write has no committed finding to report. Keep other
		// tool-error triggers available for user-defined automation.
		skip:     func(e db.TaskEvent) bool { return e.ToolIsErr && e.Tool == "report_finding" },
		runTitle: func(e db.TaskEvent) string { return fmt.Sprintf("工具触发 · %s · task#%d", e.Tool, e.TaskID) },
		ctxMsg: func(e db.TaskEvent) string {
			errTag := ""
			if e.ToolIsErr {
				errTag = "[error] "
			}
			return fmt.Sprintf("\n\n【本次由工具调用触发】\n工具: %s\n入参: %s\n返回: %s%s",
				e.Tool, trunc(e.ToolInput, 1500), errTag, trunc(e.ToolOutput, 1500))
		},
		message: func(tr *db.AgentTrigger, c string) string { return tr.ToolCallMessage + c },
		allow:   func(tr *db.AgentTrigger, e db.TaskEvent) bool { return containsFold(tr.ToolNames, e.Tool) },
	})
}

// containsFold reports whether name is in set (case-insensitive).
func containsFold(set []string, name string) bool {
	for _, s := range set {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// trunc caps s to max runes, appending an ellipsis + original length when cut.
func trunc(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + fmt.Sprintf("…(已截断,共 %d 字)", len(r))
}

func (sc *Scheduler) mustState(key string) string {
	v, _ := sc.pg.GetSchedState(key)
	return v
}

func (sc *Scheduler) firedGoalSet() map[int64]bool {
	out := map[int64]bool{}
	raw := sc.mustState(schedKeyFiredGoals)
	if raw == "" {
		return out
	}
	var ids []int64
	if json.Unmarshal([]byte(raw), &ids) == nil {
		for _, id := range ids {
			out[id] = true
		}
	}
	return out
}

func (sc *Scheduler) saveFiredGoalSet(set map[int64]bool) {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	b, _ := json.Marshal(ids)
	if err := sc.pg.SetSchedState(schedKeyFiredGoals, string(b)); err != nil {
		log.Printf("[scheduler] save fired goals failed: %v", err)
	}
}
