package agent

import "testing"

func TestBuiltinPromptFilesCarryReconBoundaries(t *testing.T) {
	if plannerDefaultTmpl == "" || workerDefaultTmpl == "" {
		t.Fatal("embedded prompt files are empty")
	}
	for _, want := range []string{"graph_overview", "extract_js_dynamic", "禁止"} {
		if !containsText(plannerDefaultTmpl, want) {
			t.Fatalf("planner prompt missing %q", want)
		}
	}
	for _, want := range []string{"run_recon", "extract_js", "extract_js_dynamic", "record_fact", "不得创建"} {
		if !containsText(workerDefaultTmpl, want) {
			t.Fatalf("worker prompt missing %q", want)
		}
	}
}

func containsText(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
