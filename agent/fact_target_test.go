package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
)

func TestRecordFactCarriesServiceHost(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("fact target", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	status := 200
	if _, err := d.Assets().UpsertHTTPService(db.UpsertHTTPServiceReq{URL: "http://127.0.0.1:3081", TaskID: task.ID, StatusCode: &status}); err != nil {
		t.Fatal(err)
	}
	// Read the inserted id through the DSL used by the real worker.
	found, err := d.Assets().QueryByType("service", 10, 0)
	if err != nil || len(found) == 0 {
		t.Fatalf("service asset: %v %+v", err, found)
	}
	tsx := NewToolSet(d.Exploration(task.ExplorationID), "work#1")
	tsx.SetAssetStore(d.Assets(), d.Companies())
	tsx.SetTaskID(task.ID)
	raw, _ := json.Marshal(map[string]any{
		"summary": "observed target", "intent_id": 0,
		"asset_ids": []int64{found[0].ID},
	})
	out, err := tsx.recordFact().Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("record_fact: %v", err)
	}
	txt := out.Flatten()
	var id int64
	if _, err := fmt.Sscanf(txt, "fact recorded: %d", &id); err != nil {
		t.Fatalf("result=%s err=%v", txt, err)
	}
	node, err := d.Exploration(task.ExplorationID).GetNode(id)
	if err != nil || node == nil {
		t.Fatalf("get fact: %v", err)
	}
	var p map[string]any
	_ = json.Unmarshal(node.Payload, &p)
	if h, _ := p["host"].(string); h != "127.0.0.1:3081" {
		t.Fatalf("fact host=%q payload=%s", h, node.Payload)
	}
}
