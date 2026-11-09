package recon

import (
	"log"
	"net/http"
	"time"
)

// ResponseLimit caps how much body a provider reads from a single HTTP response.
const responseLimit = 1 << 20 // 1 MiB

// Registry holds the available providers and their enabled state. Provider
// enablement is configuration, not code: disabling a source must never require
// changing the orchestrator.
type Registry struct {
	providers []Provider
	enabled   map[string]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{enabled: map[string]bool{}}
}

// Register adds a provider and its default enabled state.
func (r *Registry) Register(p Provider, enabled bool) {
	if p == nil {
		return
	}
	r.providers = append(r.providers, p)
	r.enabled[p.Name()] = enabled
}

// SetEnabled toggles a registered provider by name.
func (r *Registry) SetEnabled(name string, enabled bool) {
	if _, ok := r.enabled[name]; ok {
		r.enabled[name] = enabled
	}
}

// Providers returns the enabled providers in registration order.
func (r *Registry) Providers() []Provider {
	out := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		if r.enabled[p.Name()] {
			out = append(out, p)
		}
	}
	return out
}

// Names returns every registered provider name (enabled or not), for tests/UI.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Name())
	}
	return out
}

// Config configures the recon orchestrator and default providers. Values come
// from ARTEX_REDTEAM_RECON_* environment variables (see ConfigFromEnv); zero
// values are replaced by conservative defaults.
type Config struct {
	Enabled          []string      // provider names; default ["active"]
	Concurrency      int           // active probe workers; default 10, max 32
	HTTPTimeout      time.Duration // per-request timeout; default 10s
	ProviderTimeout  time.Duration // whole-provider timeout; default 3m
	CTURL            string        // legacy crt.sh endpoint override
	CTBackends       []string      // ordered CT backends; default "crt.name,crt.sh,certspotter"
	CTNameURL        string        // crt.name endpoint
	CertSpotterURL   string        // Cert Spotter endpoint
	CertSpotterToken string        // optional Cert Spotter API token
	Wordlist         []string      // builtin dictionary suffixes; default DefaultWordlist
	FOFA             FOFAConfig
	Dynamic          DynamicConfig // runtime browser capture; default OFF
}

// DefaultWordlist is the small built-in dictionary used by the active provider
// in addition to certificate transparency results. Deliberately tiny: M1 is not
// a brute-force product.
var DefaultWordlist = []string{"www", "mail", "api", "admin", "dev", "test", "vpn", "git", "cloud", "portal"}

// defaultCTBackends is the preference order chosen from the M2 benchmark
// (scripts/recon-ct-bench): certspotter 10/10 (60 ms), crt.sh 9/10 but 10-100x
// more names (and 45 s tail latency), crt.name 10/10 but 0 names from CI egress.
// Reliability/fast path first; set ARTEX_REDTEAM_RECON_CT_BACKENDS=crt.sh,... to
// prefer coverage over latency.
const defaultCTBackends = "certspotter,crt.sh,crt.name"

// FOFAConfig configures the optional external intel provider (default OFF).
type FOFAConfig struct {
	Enabled    bool
	BaseURL    string
	Email      string
	Key        string
	MaxResults int
}

// defaultFactories lets each provider file register itself (init). This keeps
// the abstraction commit independently compilable/revertable: adding or removing
// a provider file never leaves a dangling reference in the registry.
var defaultFactories []func(cfg Config, client *http.Client) Provider

// DefaultRegistry builds the registry from config. Only providers whose factory
// returned non-nil are registered; enablement is decided by Config.Enabled
// (default: active) plus the FOFA-specific Enabled switch.
func DefaultRegistry(cfg Config, client *http.Client) *Registry {
	if client == nil {
		client = defaultHTTPClient(cfg.HTTPTimeout)
	}
	enabled := cfg.Enabled
	if len(enabled) == 0 {
		enabled = []string{ProviderActive}
	}
	if cfg.FOFA.Enabled {
		enabled = append(enabled, ProviderFOFA)
	}
	if cfg.Dynamic.Enabled {
		enabled = append(enabled, ProviderDynamic)
	}
	on := func(name string) bool {
		for _, e := range enabled {
			if e == name {
				return true
			}
		}
		return false
	}
	r := NewRegistry()
	for _, factory := range defaultFactories {
		p := factory(cfg, client)
		if p == nil {
			continue
		}
		r.Register(p, on(p.Name()))
	}
	return r
}

func defaultHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

// logProviderf logs a degraded provider without aborting the run.
func logProviderf(provider, format string, args ...any) {
	log.Printf("[recon] provider=%s "+format, append([]any{provider}, args...)...)
}
