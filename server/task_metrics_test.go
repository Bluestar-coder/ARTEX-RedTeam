package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

func TestGetTaskReturnsGoalMetrics(t *testing.T) {
	m, err := NewManager(t.TempDir(), "", "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	s := newTestServer(t, m, t.TempDir(), t.TempDir(), t.TempDir())
	task, err := m.CreateTask("metrics", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "one goal"}, 1, "open", "system", nil); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID, nil)
	req.SetPathValue("id", task.ID)
	rec := httptest.NewRecorder()
	s.getTask(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var dto TaskDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.GoalsTotal != 1 || dto.GoalsMet != 0 {
		t.Fatalf("goals_total=%d goals_met=%d, want 1/0", dto.GoalsTotal, dto.GoalsMet)
	}
}
