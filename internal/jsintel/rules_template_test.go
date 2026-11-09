package jsintel

import "testing"

func TestTemplateStringRule(t *testing.T) {
	res := extractOne(t, "fetch(`/api/user/${userId}/detail`, {method: \"GET\"}); axios.get(`/v1/${res}/detail`);")
	it := findIface(res, "GET", "/api/user/{userId}/detail")
	if it == nil {
		t.Fatalf("template fetch missing: %+v", res.Interfaces)
	}
	if it.Confidence != ConfidenceMedium {
		t.Fatalf("template confidence=%s, want medium", it.Confidence)
	}
	if !contains(it.Params, "userId") {
		t.Fatalf("template params=%v", it.Params)
	}
	if findIface(res, "GET", "/v1/{res}/detail") == nil {
		t.Fatalf("axios template missing: %+v", res.Interfaces)
	}
}

func TestUnresolvedExpressionMarkedManual(t *testing.T) {
	res := extractOne(t, `fetch(this.baseUrl + "/search"); fetch(a + b + "/x");`)
	manual := false
	for _, o := range res.Obfuscations {
		if o.Reason == "needs_manual" {
			manual = true
		}
	}
	if !manual {
		t.Fatalf("unresolved prefix should be flagged needs_manual: %+v", res.Obfuscations)
	}
	if it := findIface(res, "unknown", "/search"); it != nil {
		t.Fatalf("fabricated interface from unresolved base: %+v", it)
	}
}
