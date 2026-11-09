package jsintel

import "testing"

func TestConfigObjectRule(t *testing.T) {
	res := extractOne(t, `someClient.request({url: "/api/config/item", method: "post", data: d});`)
	it := findIface(res, "POST", "/api/config/item")
	if it == nil {
		t.Fatalf("config object missing: %+v", res.Interfaces)
	}
	if it.Confidence != ConfidenceHigh {
		t.Fatalf("config object confidence=%s, want high", it.Confidence)
	}
}
