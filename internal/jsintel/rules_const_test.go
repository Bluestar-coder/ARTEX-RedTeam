package jsintel

import "testing"

func TestOneLevelConstantFoldRule(t *testing.T) {
	res := extractOne(t, `const BASE = "/api"; fetch(BASE + "/user");`)
	if it := findIface(res, "unknown", "/api/user"); it == nil || it.Confidence != ConfidenceMedium {
		t.Fatalf("const fold missing/medium: %+v", res.Interfaces)
	}
	res2 := extractOne(t, `const BASE = "/api"; const USERS = "/users"; fetch(BASE + USERS + "/user");`)
	if it := findIface(res2, "unknown", "/api{USERS}/user"); it != nil && it.Confidence == ConfidenceHigh {
		t.Fatalf("two-level fold should not be high confidence: %+v", it)
	}
}
