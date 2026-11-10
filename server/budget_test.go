package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

func TestBudgetExhaustedSettlesTaskAsTerminal(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := newTestServer(t, m, t.TempDir(), t.TempDir(), t.TempDir())
	task, err := m.CreateTaskWithOptions("budget", "goal", db.TaskCreateOptions{ToolCallBudget: 10, LLMCallBudget: 10})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		task.AddToolCall()
	}
	if !task.BudgetExceeded() {
		t.Fatal("task should be over budget")
	}
	if !s.engine.budgetExceeded(task) {
		t.Fatal("budget gate did not trigger")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !isTerminalStatus(m.TaskStatus(task.ID)) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := m.TaskStatus(task.ID); got != "budget_exhausted" {
		t.Fatalf("status=%q, want budget_exhausted", got)
	}
	// The task DTO must carry both the terminal status and the counters.
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID, nil)
	req.SetPathValue("id", task.ID)
	rec := httptest.NewRecorder()
	s.getTask(rec, req)
	var dto TaskDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Status != "budget_exhausted" || dto.ToolCalls < 10 || dto.ToolCallBudget != 10 || dto.LLMCallBudget != 10 {
		t.Fatalf("dto=%+v", dto)
	}
}

func TestBudgetMarkIsIdempotent(t *testing.T) {
	task := &Task{ToolCallBudget: 1, LLMCallBudget: 1}
	task.AddToolCall()
	if !task.BudgetExceeded() || task.MarkBudgetStop() != true || task.MarkBudgetStop() != false {
		t.Fatal("budget stop must be idempotent and reached")
	}
}

func TestCreateTaskPersistsBudgets(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	task, err := m.CreateTaskWithOptions("budget api", "goal", db.TaskCreateOptions{ToolCallBudget: 7, LLMCallBudget: 9})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	reloaded, err := m.PG().GetTask(id)
	if err != nil || reloaded == nil {
		t.Fatalf("reload err=%v task=%+v", err, reloaded)
	}
	if reloaded.ToolCallBudget != 7 || reloaded.LLMCallBudget != 9 {
		t.Fatalf("budgets not persisted: %+v", reloaded)
	}
}
