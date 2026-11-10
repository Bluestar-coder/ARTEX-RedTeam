package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunReconRejectsClearedScope(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("scope cleared", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID := task.ID
	if _, err := d.Assets().AddAgentScope(taskID, "root_domain", "example.com", "test", "manual"); err != nil {
		t.Fatal(err)
	}
	scopes, err := d.Assets().ListTaskScope(taskID)
	if err != nil || len(scopes) != 1 {
		t.Fatalf("scopes=%+v err=%v", scopes, err)
	}
	if ok, err := d.Assets().DeleteTaskScope(taskID, scopes[0].ID); err != nil || !ok {
		t.Fatalf("delete scope ok=%v err=%v", ok, err)
	}
	tsx := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	tsx.SetAssetStore(d.Assets(), d.Companies())
	tsx.SetTaskID(taskID)
	raw, _ := json.Marshal(map[string]any{"domain": "example.com"})
	out, err := tsx.runRecon().Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("run_recon call: %v", err)
	}
	if txt := out.Flatten(); !strings.Contains(txt, "没有可用网络 scope") {
		t.Fatalf("expected scope guard error after clearing, got: %s", txt)
	}
}

func TestRunReconRejectsTaskWithoutNetworkScope(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("scope guard", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	tsx := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	tsx.SetAssetStore(d.Assets(), d.Companies())
	tsx.SetTaskID(task.ID)
	raw, _ := json.Marshal(map[string]any{"domain": "example.com"})
	out, err := tsx.runRecon().Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("run_recon call: %v", err)
	}
	if txt := out.Flatten(); !strings.Contains(txt, "没有可用网络 scope") {
		t.Fatalf("expected scope guard error, got: %s", txt)
	}
}
