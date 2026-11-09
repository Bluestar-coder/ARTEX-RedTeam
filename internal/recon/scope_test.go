package recon

import "testing"

func TestScopeAllowAndExcludes(t *testing.T) {
	sc, err := NewScope("example.com", []string{"example.com", "corp.example.com"}, []string{"admin.example.com", "10.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		value string
		ok    bool
	}{
		{"example.com", true},
		{"www.example.com", true},
		{"a.b.example.com", true},
		{"corp.example.com", true},
		{"evil.com", false},
		{"example.com.evil.com", false},
		{"admin.example.com", false},
		{"x.admin.example.com", false},
		{"https://WWW.Example.com/path", true},
	}
	for _, c := range cases {
		ok, reason := sc.Allow(c.value)
		if ok != c.ok {
			t.Errorf("Allow(%q)=%v reason=%q, want %v", c.value, ok, reason, c.ok)
		}
		if !c.ok && reason == "" {
			t.Errorf("Allow(%q) rejected without reason", c.value)
		}
	}
}

func TestScopeIPAndInvalid(t *testing.T) {
	sc, err := NewScope("example.com", nil, []string{"192.0.2.5"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := sc.AllowIP("192.0.2.5"); ok {
		t.Fatal("excluded IP accepted")
	}
	if ok, _ := sc.AllowIP("198.51.100.7"); !ok {
		t.Fatal("normal IP rejected")
	}
	if ok, reason := sc.Allow("not a domain"); ok || reason == "" {
		t.Fatalf("invalid host accepted: %v %q", ok, reason)
	}
	if _, err := NewScope("", nil, nil); err == nil {
		t.Fatal("empty seed should fail")
	}
}
