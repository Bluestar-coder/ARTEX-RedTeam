package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestVerifyJWTRejectsStalePasswordVersion covers the revocation primitive: a
// token issued under one password must stop working as soon as the stored
// password hash (and therefore the version tag) changes.
func TestVerifyJWTRejectsStalePasswordVersion(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	oldPV := passwordVersion("$2a$10$oldsaltoldhash")
	newPV := passwordVersion("$2a$10$newsaltnewhash")
	if oldPV == newPV || oldPV == "" || newPV == "" {
		t.Fatalf("passwordVersion must differ: old=%q new=%q", oldPV, newPV)
	}

	tok, err := signJWTWithPV(key, oldPV)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(tok, key, oldPV) {
		t.Fatal("token should verify against its own version")
	}
	if verifyJWT(tok, key, newPV) {
		t.Fatal("token must be rejected after the password version changes")
	}

	// Legacy tokens (issued before the pv claim existed) still verify on
	// signature alone for a 7-day transition window.
	legacy, err := signJWT(key)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(legacy, key, newPV) {
		t.Fatal("legacy token without pv should still verify on signature")
	}

	// A token signed with another key never verifies.
	if verifyJWT(tok, []byte("ffffffffffffffffffffffffffffffff"), oldPV) {
		t.Fatal("token signed with a different key must be rejected")
	}
}

// TestPasswordVersionIsNotTheHash keeps the claim non-reversible: it is derived
// from the bcrypt hash, never equal to it.
func TestPasswordVersionIsNotTheHash(t *testing.T) {
	hash := "$2a$10$abcdefghijklmnopqrstuv"
	if got := passwordVersion(hash); got == hash || len(got) != 16 {
		t.Fatalf("passwordVersion = %q", got)
	}
	if passwordVersion("") != "" {
		t.Fatal("empty hash must map to empty version")
	}
}

// TestPublicAllowlistIsExact pins the auth allowlist: only the three auth entry
// points and /api/health may be reached without a token. A prefix match here was
// what let change-password and any future auth handler sit outside the gate.
func TestPublicAllowlistIsExact(t *testing.T) {
	for _, p := range []string{"/api/auth/status", "/api/auth/init", "/api/auth/login", "/api/health"} {
		if _, ok := publicAPIPaths[p]; !ok {
			t.Fatalf("%s should be public", p)
		}
	}
	for _, p := range []string{"/api/auth/change-password", "/api/auth/anything", "/api/auth/", "/api/tasks"} {
		if _, ok := publicAPIPaths[p]; ok {
			t.Fatalf("%s must not be public", p)
		}
	}
}

func TestRequireAuthRejectsUnknownAuthPaths(t *testing.T) {
	s := &Server{jwtKey: []byte("0123456789abcdef0123456789abcdef")}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })

	cases := []struct {
		path string
		want int
	}{
		{"/api/auth/status", 204},
		{"/api/auth/init", 204},
		{"/api/auth/change-password", 401},
		{"/api/auth/evil", 401},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.requireAuth(ok).ServeHTTP(rec, httptest.NewRequest("POST", c.path, nil))
		if rec.Code != c.want {
			t.Fatalf("%s: status=%d want %d", c.path, rec.Code, c.want)
		}
	}
}

// TestHealthHidesFingerprint checks that an unauthenticated probe gets liveness
// only, while an authenticated one (the update card polls it with the cookie)
// still sees the version it needs to detect a completed upgrade.
func TestHealthHidesFingerprint(t *testing.T) {
	s := &Server{jwtKey: []byte("0123456789abcdef0123456789abcdef")}
	BuildVersion = "v9.9.9"

	rec := httptest.NewRecorder()
	s.health(rec, httptest.NewRequest("GET", "/api/health", nil))
	var anon map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &anon); err != nil {
		t.Fatal(err)
	}
	if _, ok := anon["version"]; ok {
		t.Fatalf("unauthenticated health leaked version: %v", anon)
	}
	if _, ok := anon["service"]; ok {
		t.Fatalf("unauthenticated health leaked service name: %v", anon)
	}

	tok, err := signJWTWithPV(s.jwtKey, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec = httptest.NewRecorder()
	s.health(rec, req)
	var auth map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	if auth["version"] != "v9.9.9" {
		t.Fatalf("authenticated health should expose version, got %v", auth)
	}
}

func TestIPLimiterBurstThenRefill(t *testing.T) {
	l := newIPLimiter(2, 3) // 2 tokens/s, burst 3
	for i := 0; i < 3; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("request %d should pass within burst", i+1)
		}
	}
	if l.allow("1.2.3.4") {
		t.Fatal("4th immediate request should be limited")
	}
	if !l.allow("5.6.7.8") {
		t.Fatal("another IP must not be affected")
	}
	time.Sleep(600 * time.Millisecond) // ~1.2 tokens at 2/s
	if !l.allow("1.2.3.4") {
		t.Fatal("bucket should refill over time")
	}
}

func TestAuthRateLimitEnvOverride(t *testing.T) {
	t.Setenv("ARTEX_REDTEAM_AUTH_RATE_PER_MIN", "600")
	rate, burst := authRateLimit()
	if rate < 9.9 || burst != 10 {
		t.Fatalf("rate=%v burst=%v", rate, burst)
	}
	t.Setenv("ARTEX_REDTEAM_AUTH_RATE_PER_MIN", "not-a-number")
	rate, _ = authRateLimit()
	if rate > 1 {
		t.Fatalf("invalid override should fall back to 12/min, got %v", rate)
	}
}

// TestValidateStdioCommand blocks the webshell shape while leaving legitimate
// MCP server launchers (npx / uvx / python script) alone.
func TestValidateStdioCommand(t *testing.T) {
	cases := []struct {
		cmd     string
		args    []string
		wantErr bool
	}{
		{"sh", []string{"-c", "id"}, true},
		{"/bin/bash", nil, true},
		{"busybox", []string{"sh"}, true},
		{"powershell.exe", []string{"-Command", "whoami"}, true},
		{"cmd.exe", []string{"/c", "whoami"}, true},
		{"python3", []string{"-c", "print(1)"}, true},
		{"node", []string{"-e", "1"}, true},
		{"python3", []string{"server.py"}, false},
		{"node", []string{"server.js"}, false},
		{"env", []string{"sh", "-c", "id"}, true},
		{"env", []string{"FOO=1", "python3", "-c", "print(1)"}, true},
		{"nohup", []string{"bash", "-c", "id"}, true},
		{"env", []string{"FOO=1", "npx", "-y", "@playwright/mcp"}, false},
		{"npx", []string{"-y", "@playwright/mcp"}, false},
		{"uvx", []string{"mcp-server-git"}, false},
	}
	for _, c := range cases {
		err := validateStdioCommand(c.cmd, c.args)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s %v: err=%v wantErr=%v", c.cmd, c.args, err, c.wantErr)
		}
	}

	t.Setenv("ARTEX_REDTEAM_ALLOW_STDIO_SHELL", "1")
	if err := validateStdioCommand("sh", []string{"-c", "id"}); err != nil {
		t.Fatalf("explicit opt-in should allow shell-backed stdio: %v", err)
	}
}

// TestAuthDecodeRejectsUnknownFields pins the anti-backdoor property: a login
// body carrying extra keys (the 2026 poisoned-image used "Captcha") must fail
// closed instead of being silently accepted by a lenient decoder.
func TestAuthDecodeRejectsUnknownFields(t *testing.T) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	evil := `{"username":"x","password":"y","Captcha":"!id"}`
	if err := decodeStrict(httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(evil)), &req); err == nil {
		t.Fatal("login body with unknown field must be rejected")
	}
	good := `{"username":"x","password":"y"}`
	if err := decodeStrict(httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(good)), &req); err != nil {
		t.Fatalf("normal login body should decode: %v", err)
	}
}
