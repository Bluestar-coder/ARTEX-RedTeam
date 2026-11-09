package jsintel

import "testing"

func TestBareSuffixRule(t *testing.T) {
	res := extractOne(t, `var health = "/health.json"; var cfg = {"path": "/status.php"}; var act = "/do/thing.action";`)
	for _, path := range []string{"/health.json", "/status.php", "/do/thing.action"} {
		it := findIface(res, "unknown", path)
		if it == nil || it.Confidence != ConfidenceLow {
			t.Fatalf("bare suffix %s missing/low: %+v", path, res.Interfaces)
		}
	}
	res = extractOne(t, `var _=".json"; var g=".action";`)
	if len(res.Interfaces) != 0 {
		t.Fatalf("non-path suffix constants leaked: %+v", res.Interfaces)
	}
}
