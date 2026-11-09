package jsintel

import (
	"strings"
	"testing"
)

func analyzeOne(t *testing.T, code string) Analysis {
	t.Helper()
	return AnalyzeFiles([]FileInput{{Host: "app.example.com", PageURL: "https://app.example.com/", ScriptURL: "https://app.example.com/app.js", Code: code}})
}

func findASTIface(a Analysis, method, path string) *Interface {
	for i := range a.Interfaces {
		if a.Interfaces[i].Method == method && a.Interfaces[i].Path == path {
			return &a.Interfaces[i]
		}
	}
	return nil
}

func TestASTCallForms(t *testing.T) {
	code := `
fetch("/api/fetch", {method: "POST"});
axios.get("/api/axios");
const xhr = new XMLHttpRequest(); xhr.open("PUT", "/api/xhr");
$.get("/api/jquery", function(){});
navigator.sendBeacon("/api/beacon", data);
client.request({url: "/api/config", method: "patch"});
`
	a := analyzeOne(t, code)
	want := []struct{ method, path string }{
		{"POST", "/api/fetch"}, {"GET", "/api/axios"}, {"PUT", "/api/xhr"},
		{"GET", "/api/jquery"}, {"POST", "/api/beacon"}, {"PATCH", "/api/config"},
	}
	for _, w := range want {
		if it := findASTIface(a, w.method, w.path); it == nil {
			t.Fatalf("missing %s %s in %+v", w.method, w.path, a.Interfaces)
		}
	}
}

func TestASTMultiHopConst(t *testing.T) {
	a := analyzeOne(t, `const A = "/api"; const B = A + "/v1"; const C = B + "/users"; fetch(C + "/" + id);`)
	it := findASTIface(a, "unknown", "/api/v1/users/{id}")
	if it == nil || it.Confidence != ConfidenceMedium {
		t.Fatalf("multi-hop missing/medium: %+v", a.Interfaces)
	}
}

func TestASTCrossFileConst(t *testing.T) {
	a := AnalyzeFiles([]FileInput{
		{Host: "app.example.com", PageURL: "https://app.example.com/", ScriptURL: "https://app.example.com/const.js", Code: `const BASE = "/api";`},
		{Host: "app.example.com", PageURL: "https://app.example.com/", ScriptURL: "https://app.example.com/app.js", Code: `axios.post(BASE + "/login", body);`},
	})
	if it := findASTIface(a, "POST", "/api/login"); it == nil || it.Confidence != ConfidenceMedium {
		t.Fatalf("cross-file const missing: %+v", a.Interfaces)
	}
}

func TestASTHopLimitNeedsManual(t *testing.T) {
	a := analyzeOne(t, `
const A = "/api"; const B = A; const C = B; const D = C; const E = D; const F = E; const G = F;
fetch(G + "/users");
`)
	if len(a.Interfaces) != 0 {
		t.Fatalf("hop-limited expression must not be guessed: %+v", a.Interfaces)
	}
	found := false
	for _, n := range a.Notes {
		if n.Reason == "needs_manual" && strings.Contains(n.Detail, "exceeded_hops") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected needs_manual exceeded_hops: %+v", a.Notes)
	}
}

func TestASTConfidenceTiers(t *testing.T) {
	a := analyzeOne(t, `axios.get("/api/high"); fetch(`+"`"+`/api/${id}`+"`"+`); fetch("/api/unknown");`)
	if it := findASTIface(a, "GET", "/api/high"); it == nil || it.Confidence != ConfidenceHigh {
		t.Fatalf("high tier: %+v", it)
	}
	if it := findASTIface(a, "unknown", "/api/{id}"); it == nil || it.Confidence != ConfidenceMedium {
		t.Fatalf("medium tier: %+v", it)
	}
	if it := findASTIface(a, "unknown", "/api/unknown"); it == nil || it.Confidence != ConfidenceLow {
		t.Fatalf("low tier: %+v", it)
	}
}

func TestASTRuntimeBaseNeedsManual(t *testing.T) {
	a := analyzeOne(t, `fetch(this.baseUrl + "/search");`)
	if len(a.Interfaces) != 0 {
		t.Fatalf("runtime base must not fabricate: %+v", a.Interfaces)
	}
	found := false
	for _, n := range a.Notes {
		if n.Reason == "needs_manual" && (strings.Contains(n.Detail, "runtime") || strings.Contains(n.Detail, "unresolved")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected runtime needs_manual: %+v", a.Notes)
	}
}

func TestASTParseFailureIsReported(t *testing.T) {
	a := analyzeOne(t, `fetch(`+"`"+`/api/${`+"`")
	if len(a.ParseFails) == 0 {
		t.Fatalf("expected parse failure")
	}
}
