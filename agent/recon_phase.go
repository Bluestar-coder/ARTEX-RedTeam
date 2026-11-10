package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/db"
	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
	actool "github.com/Bluestar-coder/ARTEX-RedTeam/norma/tool"
)

// PhaseResult is the outcome of one deterministic recon step. The server's
// pre-planner phase calls these functions directly (no LLM session); errors are
// recorded and the next step still runs.
type PhaseResult struct {
	Step     string
	Duration time.Duration
	Data     map[string]any
	Err      error
	Warning  string
}

// phaseToolSet builds a ToolSet for a deterministic, non-LLM invocation. It uses
// the same tool implementations as the agents, so phase behavior and tool behavior
// cannot drift.
func phaseToolSet(as *db.AssetStore, ts *db.ExplorationStore, taskID int64) *ToolSet {
	tsx := NewToolSet(ts, "recon")
	tsx.SetAssetStore(as, as.Companies())
	tsx.SetTaskID(taskID)
	return tsx
}

// RunReconPhase executes run_recon with the supplied target (domain, IP, or
// host:port) and persists assets. It is the first deterministic phase step.
func RunReconPhase(ctx context.Context, as *db.AssetStore, taskID int64, target string, exclude []string) PhaseResult {
	start := time.Now()
	res := PhaseResult{Step: "run_recon"}
	out, err := performRecon(ctx, as, taskID, target, exclude, recon.ConfigFromEnv())
	res.Duration = time.Since(start)
	res.Data = out
	if err != nil {
		res.Err = err
		return res
	}
	if phaseNumber(out["records"]) == 0 {
		res.Warning = "run_recon 返回 0 条记录"
	}
	return res
}

// RunStaticPhase executes extract_js for the domain/asset scope. It never invokes
// the LLM; all extraction is the normal static analyzer.
func RunStaticPhase(ctx context.Context, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, domain string) PhaseResult {
	start := time.Now()
	res := PhaseResult{Step: "extract_js"}
	out, err := callPhaseTool(ctx, phaseToolSet(as, ts, taskID).extractJS(), map[string]any{
		"domain": domain, "limit": 30,
	})
	res.Duration = time.Since(start)
	res.Data = out
	if err != nil {
		res.Err = err
		return res
	}
	if phaseNumber(out["targets"]) == 0 {
		res.Warning = "没有可扫描的存活 HTTP service（extract_js 未执行）"
	}
	if phaseNumber(out["interfaces"]) == 0 && res.Warning == "" {
		res.Warning = "extract_js 未提取到接口"
	}
	return res
}

// RunDynamicPhase executes extract_js_dynamic with the tool's normal default-page
// resolution. The caller must only call it when the dynamic provider is enabled;
// it is kept separate because of browser cost/risk.
func RunDynamicPhase(ctx context.Context, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, domain string) PhaseResult {
	start := time.Now()
	res := PhaseResult{Step: "extract_js_dynamic"}
	out, err := callPhaseTool(ctx, phaseToolSet(as, ts, taskID).extractJSDynamic(), map[string]any{
		"domain": domain,
	})
	res.Duration = time.Since(start)
	res.Data = out
	if err != nil {
		res.Err = err
		return res
	}
	if msg, _ := out["error"].(string); msg != "" {
		res.Err = fmt.Errorf("%s", msg)
		return res
	}
	if phaseNumber(out["interfaces"]) == 0 {
		res.Warning = "dynamic 未捕获到接口"
	}
	return res
}

// callPhaseTool invokes a CoreTool directly. Tools return either a JSON object or
// a plain error string; both forms are normalized here so phase callers can log a
// precise failure without parsing the tool-specific text themselves.
func callPhaseTool(ctx context.Context, tool actool.CoreTool, payload any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	res, err := tool.Call(ctx, raw, nil)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(res.Flatten())
	if strings.HasPrefix(text, "{") {
		var out map[string]any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			return nil, fmt.Errorf("解析 %s 结果失败: %w", tool.Name(), err)
		}
		if e, _ := out["error"].(string); e != "" {
			return out, fmt.Errorf("%s", e)
		}
		return out, nil
	}
	if text == "" {
		return nil, fmt.Errorf("%s 返回空结果", tool.Name())
	}
	return nil, fmt.Errorf("%s", text)
}

func phaseNumber(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	}
	return 0
}
