package jsintel

import (
	"strings"
	"testing"
)

const sampleJS = `
const base = "https://app.example.com";
async function login(user, pass) {
  return fetch("/api/v1/login", {method: "POST", body: JSON.stringify({username: user, password: pass})});
}
axios.get("/api/v1/users?page=1&size=20");
axios.post("/api/v1/orders", {order_id: 1, items: items});
const xhr = new XMLHttpRequest(); xhr.open("DELETE", "/api/v1/items/${id}");
$.ajax({url: "/api/v1/upload", type: "POST", data: {file: f}});
const appid = "wx1234567890abcdef";
const apiSecret = "9f8a7b6c5d4e3f201122334455667788";
const apiKey = "YOUR_API_KEY";
const internal = "http://10.12.0.8:8080/health";
const testHost = "https://staging.example.com/api";
const mapUrl = "https://webapi.amap.com/maps?v=2.0";
const notify = "https://pay.example.com/notify_url";
const admin = "/admin/dashboard";
`

func TestExtractInterfaces(t *testing.T) {
	res := Extract("app.example.com", "https://app.example.com/", "https://app.example.com/main.js", sampleJS)
	byKey := map[string]Interface{}
	for _, it := range res.Interfaces {
		byKey[it.Method+" "+it.Path] = it
	}
	wants := []struct {
		key    string
		params []string
		tags   []string
	}{
		{"POST /api/v1/login", []string{"password", "username"}, nil},
		{"GET /api/v1/users", []string{"page", "size"}, nil},
		{"POST /api/v1/orders", []string{"items", "order_id"}, nil},
		{"DELETE /api/v1/items/{id}", []string{"id"}, nil},
		{"POST /api/v1/upload", []string{"file"}, []string{"upload"}},
	}
	for _, w := range wants {
		it, ok := byKey[w.key]
		if !ok {
			t.Fatalf("missing interface %q; got %v", w.key, keys(byKey))
		}
		for _, p := range w.params {
			if !contains(it.Params, p) {
				t.Errorf("%s params=%v missing %q", w.key, it.Params, p)
			}
		}
		for _, tag := range w.tags {
			if !contains(it.Tags, tag) {
				t.Errorf("%s tags=%v missing %q", w.key, it.Tags, tag)
			}
		}
		if len(it.Sources) == 0 || it.Sources[0].Line == 0 || it.Sources[0].Snippet == "" {
			t.Errorf("%s missing source ref: %+v", w.key, it.Sources)
		}
	}
}

func TestExtractSensitiveAndFalsePositives(t *testing.T) {
	res := Extract("app.example.com", "https://app.example.com/", "https://app.example.com/main.js", sampleJS)
	byKind := map[string][]Sensitive{}
	for _, s := range res.Sensitives {
		byKind[s.Kind] = append(byKind[s.Kind], s)
	}
	if len(byKind["hardcoded_secret"]) != 2 { // appid + apiSecret; YOUR_API_KEY must be filtered
		t.Fatalf("secrets=%+v", byKind["hardcoded_secret"])
	}
	for _, s := range byKind["hardcoded_secret"] {
		if s.Value == "YOUR_API_KEY" || s.Value == "9f8a7b6c5d4e3f201122334455667788" {
			t.Fatalf("secret not masked/filtered: %+v", s)
		}
		if s.Sources[0].Snippet == "" {
			t.Fatalf("secret without snippet: %+v", s)
		}
	}
	if len(byKind["internal_address"]) == 0 || byKind["internal_address"][0].Value != "10.12.0.8:8080" {
		t.Fatalf("internal=%+v", byKind["internal_address"])
	}
	if len(byKind["test_env_domain"]) == 0 || !strings.Contains(byKind["test_env_domain"][0].Value, "staging.example.com") {
		t.Fatalf("test env=%+v", byKind["test_env_domain"])
	}
	foundSDK := false
	for _, s := range byKind["third_party_sdk"] {
		if s.Rule == "sdk:amap" {
			foundSDK = true
		}
	}
	if !foundSDK {
		t.Fatalf("sdk=%+v", byKind["third_party_sdk"])
	}
	if len(byKind["upload_hint"]) == 0 || len(byKind["payment_callback"]) == 0 || len(byKind["admin_path"]) == 0 {
		t.Fatalf("hints upload=%d payment=%d admin=%d", len(byKind["upload_hint"]), len(byKind["payment_callback"]), len(byKind["admin_path"]))
	}
}

func TestExtractDedupeAcrossOccurrences(t *testing.T) {
	text := `fetch("/api/v1/same"); fetch("/api/v1/same", {method:"GET"});`
	res := Extract("h.example.com", "https://h.example.com/", "https://h.example.com/a.js", text)
	var same []Interface
	for _, it := range res.Interfaces {
		if it.Path == "/api/v1/same" {
			same = append(same, it)
		}
	}
	if len(same) != 2 { // unknown vs GET are different keys by design
		t.Fatalf("same=%+v", same)
	}
	totalSources := 0
	for _, it := range same {
		totalSources += len(it.Sources)
	}
	if totalSources != 4 { // 2 path literals + fetch(unknown) merge; GET is separate
		t.Fatalf("sources=%+v total=%d", same, totalSources)
	}
}

func TestExtractObfuscation(t *testing.T) {
	text := `var _0x1a2b=1,_0x2b3c=2,_0x3c4d=3,_0x4d5e=4,_0x5e6f=5; eval(_0x1a2b); atob("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ejAxMjM0NTY3ODk="); fetch("/api/"+x);`
	res := Extract("h.example.com", "https://h.example.com/", "https://h.example.com/ob.js", text)
	reasons := map[string]bool{}
	for _, o := range res.Obfuscations {
		reasons[o.Reason] = true
	}
	for _, want := range []string{"hex_identifiers", "dynamic_eval", "base64_decode", "path_concatenation"} {
		if !reasons[want] {
			t.Errorf("missing obfuscation reason %q (got %v)", want, reasons)
		}
	}
}

func keys(m map[string]Interface) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
