package recon

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// ConfigFromEnv reads ARTEX_REDTEAM_RECON_* settings. All values are optional;
// reconnaissance works out of the box with the active provider only.
func ConfigFromEnv() Config {
	cfg := Config{
		Enabled:          splitCSV(env("RECON_ENABLED", "")),
		Concurrency:      envInt("RECON_CONCURRENCY", 10),
		HTTPTimeout:      time.Duration(envInt("RECON_TIMEOUT_SEC", 10)) * time.Second,
		CTURL:            env("RECON_CT_URL", "https://crt.sh"),
		CTBackends:       splitCSV(env("RECON_CT_BACKENDS", "")),
		CTNameURL:        env("RECON_CT_CRTNAME_URL", "https://crt.name/v1/search"),
		CertSpotterURL:   env("RECON_CT_CERTSPOTTER_URL", "https://api.certspotter.com/v1/issuances"),
		CertSpotterToken: env("RECON_CT_CERTSPOTTER_TOKEN", ""),
		Wordlist:         splitCSV(env("RECON_WORDLIST", "")),
		Dynamic:          DynamicConfigFromEnv(),
		FOFA: FOFAConfig{
			Enabled:    envBool("RECON_FOFA_ENABLED", false),
			BaseURL:    env("RECON_FOFA_URL", "https://fofa.info/api/v1/search/all"),
			Email:      env("RECON_FOFA_EMAIL", ""),
			Key:        env("RECON_FOFA_KEY", ""),
			MaxResults: envInt("RECON_FOFA_MAX", 200),
		},
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 10
	}
	if cfg.Concurrency > 32 {
		cfg.Concurrency = 32
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}
	cfg.ProviderTimeout = 3 * time.Minute
	if len(cfg.Wordlist) == 0 {
		cfg.Wordlist = append([]string(nil), DefaultWordlist...)
	}
	if cfg.FOFA.MaxResults <= 0 {
		cfg.FOFA.MaxResults = 200
	}
	return cfg
}

func env(suffix, def string) string {
	for _, p := range []string{"ARTEX_REDTEAM_", "ARTEX_"} {
		if v := strings.TrimSpace(os.Getenv(p + suffix)); v != "" {
			return v
		}
	}
	return def
}

func envInt(suffix string, def int) int {
	if v := env(suffix, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(suffix string, def bool) bool {
	if v := env(suffix, ""); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
