package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPostgresDSNPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	os.WriteFile(cfgPath, []byte(`{"database":{"host":"10.1.2.3","port":6000,"user":"u","password":"p","dbname":"d","sslmode":"require"}}`), 0o644)
	t.Setenv("ARTEX_REDTEAM_CONFIG", cfgPath)

	// no env DSN → built from config file fields
	t.Setenv("ARTEX_REDTEAM_PG_DSN", "")
	got, _, err := PostgresDSN()
	want := "postgres://u:p@10.1.2.3:6000/d?sslmode=require"
	if err != nil || got != want {
		t.Fatalf("from file: got %q err %v want %q", got, err, want)
	}

	// env wins over config file
	t.Setenv("ARTEX_REDTEAM_PG_DSN", "postgres://envwins/x")
	if got, _, err := PostgresDSN(); err != nil || got != "postgres://envwins/x" {
		t.Fatalf("env should win, got %q err %v", got, err)
	}

	// no env, no file → error (no built-in default)
	t.Setenv("ARTEX_REDTEAM_PG_DSN", "")
	t.Setenv("ARTEX_REDTEAM_CONFIG", filepath.Join(dir, "nope.json"))
	if got, _, err := PostgresDSN(); err == nil {
		t.Fatalf("missing config should error, got %q", got)
	}

	// full dsn in config file is used verbatim
	os.WriteFile(cfgPath, []byte(`{"database":{"dsn":"postgres://full/dsn"}}`), 0o644)
	t.Setenv("ARTEX_REDTEAM_CONFIG", cfgPath)
	if got, _, err := PostgresDSN(); err != nil || got != "postgres://full/dsn" {
		t.Fatalf("file dsn verbatim, got %q err %v", got, err)
	}
}

// TestEnvLegacyFallback keeps the pre-rename names working: a deployment that
// still exports ARTEX_PG_DSN must keep connecting after the rebrand.
func TestEnvLegacyFallback(t *testing.T) {
	t.Setenv("ARTEX_REDTEAM_PG_DSN", "")
	t.Setenv("ARTEX_PG_DSN", "postgres://legacy/db")
	if got := Env("PG_DSN"); got != "postgres://legacy/db" {
		t.Fatalf("legacy fallback = %q", got)
	}
	t.Setenv("ARTEX_REDTEAM_PG_DSN", "postgres://new/db")
	if got := Env("PG_DSN"); got != "postgres://new/db" {
		t.Fatalf("new name should win, got %q", got)
	}
}

// TestAdoptLegacyDataDir covers the in-place upgrade path: a pre-rename data/
// directory next to the binary is renamed to data-redteam so jwt.key, the
// traffic index and workspaces survive the rebrand.
func TestAdoptLegacyDataDir(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "data")
	fresh := filepath.Join(base, "data-redteam")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "jwt.key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, migratedFrom := AdoptLegacyDataDir(fresh)
	if got != fresh || migratedFrom != legacy {
		t.Fatalf("got dir=%q from=%q, want %q from %q", got, migratedFrom, fresh, legacy)
	}
	if _, err := os.Stat(filepath.Join(fresh, "jwt.key")); err != nil {
		t.Fatalf("jwt.key should move with the directory: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy dir should be gone, stat err=%v", err)
	}

	// A second call is a no-op: the new dir already exists.
	if got, from := AdoptLegacyDataDir(fresh); got != fresh || from != "" {
		t.Fatalf("second call: dir=%q from=%q", got, from)
	}

	// An explicit custom -data path is never rewritten.
	custom := filepath.Join(base, "somewhere-else")
	if got, from := AdoptLegacyDataDir(custom); got != custom || from != "" {
		t.Fatalf("custom dir touched: dir=%q from=%q", got, from)
	}
}
