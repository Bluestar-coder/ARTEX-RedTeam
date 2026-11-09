package recon

import (
	"context"
	"strings"
	"testing"
)

func TestDynamicDisabledByDefault(t *testing.T) {
	for _, k := range []string{
		"ARTEX_REDTEAM_RECON_DYNAMIC_ENABLED", "ARTEX_RECON_DYNAMIC_ENABLED",
		"ARTEX_REDTEAM_RECON_DYNAMIC_PROXY", "ARTEX_RECON_DYNAMIC_PROXY",
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy",
	} {
		t.Setenv(k, "")
	}
	cfg := ConfigFromEnv()
	if cfg.Dynamic.Enabled {
		t.Fatal("dynamic provider must be default OFF")
	}
	if cfg.Dynamic.Proxy != "" {
		t.Fatalf("dynamic proxy should be empty without explicit egress: %q", cfg.Dynamic.Proxy)
	}
	reg := DefaultRegistry(cfg, nil)
	for _, name := range reg.Names() {
		if name == ProviderDynamic {
			t.Fatal("dynamic provider must not be registered when disabled")
		}
	}
}

func TestDynamicEnabledRequiresProxy(t *testing.T) {
	cfg := DynamicConfig{Enabled: true}
	p := NewDynamicProvider(cfg)
	scope, _ := NewScope("example.com", nil, nil)
	_, err := p.Capture(context.Background(), "https://example.com/", scope)
	if err == nil || !strings.Contains(err.Error(), "requires an egress proxy") {
		t.Fatalf("want proxy-required error, got %v", err)
	}
}

func TestParamNamesNoValues(t *testing.T) {
	jsonBody := `{"username":"alice","password":"secret","profile":{"email":"a@b.c"},"roles":["admin"]}`
	got := paramNames(jsonBody)
	want := []string{"password", "profile", "profile.email", "roles", "username"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("json param names=%v want %v", got, want)
	}
	for _, n := range got {
		if strings.Contains(n, "alice") || strings.Contains(n, "secret") || strings.Contains(n, "a@b.c") {
			t.Fatalf("value leaked into names: %v", got)
		}
	}
	form := paramNames("token=abc&page=2&password=hunter2")
	if strings.Join(form, ",") != "page,password,token" {
		t.Fatalf("form param names=%v", form)
	}
}

func TestCustomHeaderNamesOnly(t *testing.T) {
	got := customHeaderNames(map[string]any{"User-Agent": "Mozilla/5.0", "Accept": "*/*", "X-Token": "super-secret-token-value", "X-Sign": "signature-value", "Content-Type": "application/json", "X-Request-Id": "abc", "Proxy-Authorization": "Basic eA=="})
	if strings.Join(got, ",") != "proxy-authorization,x-request-id,x-sign,x-token" {
		t.Fatalf("custom header names=%v", got)
	}
	for _, n := range got {
		if strings.Contains(n, "super-secret") || strings.Contains(n, "signature-value") {
			t.Fatalf("header value leaked into names: %v", got)
		}
	}
}

// helpers keep the tests readable without exposing network.Headers construction
// everywhere.
