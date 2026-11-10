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

func TestScopeAcceptsIPPortLocalhostAndRejectsMalformed(t *testing.T) {
	valid := []struct {
		seed string
		host string
		port int
	}{
		{"127.0.0.1:3000", "127.0.0.1", 3000},
		{"localhost", "localhost", 0},
		{"localhost:8080", "localhost", 8080},
		{"[::1]:8080", "::1", 8080},
		{"example.com", "example.com", 0},
		{"10.0.0.1", "10.0.0.1", 0},
	}
	for _, c := range valid {
		sc, err := NewScope(c.seed, nil, nil)
		if err != nil {
			t.Errorf("NewScope(%q) failed: %v", c.seed, err)
			continue
		}
		if sc.Seed != c.host || sc.SeedPort != c.port {
			t.Errorf("NewScope(%q) seed=%q port=%d, want %q/%d", c.seed, sc.Seed, sc.SeedPort, c.host, c.port)
		}
		if ok, reason := sc.Allow(c.seed); !ok {
			t.Errorf("Allow(%q) rejected: %s", c.seed, reason)
		}
	}
	invalid := []string{"foo..bar", "http://", "", "999.999.999.999", "example.com:70000", "1.2.3.4:abc"}
	for _, seed := range invalid {
		if _, err := NewScope(seed, nil, nil); err == nil {
			t.Errorf("NewScope(%q) accepted malformed host", seed)
		}
	}

	sc, err := NewScope("127.0.0.1:3000", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := sc.Allow("127.0.0.1"); !ok {
		t.Fatal("seed IP should be allowed without port")
	}
	if ok, reason := sc.Allow("10.0.0.1"); ok {
		t.Fatalf("out-of-scope IP accepted: %s", reason)
	}
}
