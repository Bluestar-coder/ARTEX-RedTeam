package jsintel

import "testing"

func TestAxiosBaseURLRule(t *testing.T) {
	res := extractOne(t, `const api = axios.create({baseURL: "/api", timeout: 1000}); api.get("/user"); api.post("/login", {password: "x"});`)
	if it := findIface(res, "GET", "/api/user"); it == nil || it.Confidence != ConfidenceMedium {
		t.Fatalf("axios baseURL GET missing: %+v", res.Interfaces)
	}
	if findIface(res, "POST", "/api/login") == nil {
		t.Fatalf("axios baseURL POST missing: %+v", res.Interfaces)
	}
}

func TestAxiosConstFoldRule(t *testing.T) {
	res := extractOne(t, `const BASE = "/api"; axios.get(BASE + "/orders");`)
	if it := findIface(res, "GET", "/api/orders"); it == nil || it.Confidence != ConfidenceMedium {
		t.Fatalf("axios const fold missing/medium: %+v", res.Interfaces)
	}
}
