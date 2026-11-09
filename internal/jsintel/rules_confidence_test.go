package jsintel

import "testing"

func TestConfidenceRanking(t *testing.T) {
	res := extractOne(t, `axios.get("/api/literal"); fetch("/api/unknown-method");`)
	if it := findIface(res, "GET", "/api/literal"); it == nil || it.Confidence != ConfidenceHigh {
		t.Fatalf("literal axios should be high: %+v", it)
	}
	if it := findIface(res, "unknown", "/api/unknown-method"); it == nil || it.Confidence != ConfidenceLow {
		t.Fatalf("unknown method should be low: %+v", it)
	}
}
