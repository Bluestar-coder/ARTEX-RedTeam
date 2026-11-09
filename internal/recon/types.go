// Package recon is the provider-agnostic collection layer for M1 reconnaissance:
// a provider turns a seed into standardized records, the orchestrator applies
// scope checks, deduplicates and returns records; persistence (assets table) is
// done by callers through the db package.
//
// Adding a new source = implement Provider and register it in DefaultRegistry.
package recon

import (
	"context"
	"time"
)

// RecordType is the normalized asset type a record maps to.
type RecordType string

const (
	TypeRootDomain RecordType = "root_domain"
	TypeSubdomain  RecordType = "subdomain"
	TypeIP         RecordType = "ip"
	TypeService    RecordType = "service"
)

// Record is the standardized output of every provider. Callers persist it into
// the asset graph; providers never write to the database themselves.
type Record struct {
	Type       RecordType `json:"type"`
	Value      string     `json:"value"`            // domain (root/subdomain), IP, or URL (service)
	Parent     string     `json:"parent,omitempty"` // root domain for subdomains / host for IP & service
	Source     string     `json:"source"`           // provider name (e.g. "active:ct", "fofa")
	ObservedAt time.Time  `json:"observed_at"`
	RawRef     string     `json:"raw_ref,omitempty"` // provider-side reference (crt.sh entry, API query, probed URL)

	// Service facts (only set for TypeService).
	IP            string   `json:"ip,omitempty"`
	StatusCode    *int     `json:"status_code,omitempty"`
	ContentLength *int64   `json:"content_length,omitempty"`
	Title         string   `json:"title,omitempty"`
	FaviconMMH3   string   `json:"favicon_mmh3,omitempty"`
	Technologies  []string `json:"technologies,omitempty"`

	Extra map[string]any `json:"extra,omitempty"`
}

// Request is the input every provider receives. Providers MUST run Scope checks
// before issuing any network request for a candidate.
type Request struct {
	Seed    string
	Scope   Scope
	Now     time.Time
	MaxHits int // optional per-provider result cap (0 = provider default)
}

// Provider is one collection source. Run must be safe to call with a context
// deadline; a provider error is logged and degrades that source only.
type Provider interface {
	Name() string
	Run(ctx context.Context, req Request) ([]Record, error)
}

// ProviderName identifies a provider in logs/config. The active probe provider is
// "active"; the external intel provider is "fofa".
const (
	ProviderActive = "active"
	ProviderFOFA   = "fofa"
)

// Stat is the per-provider outcome of one orchestrator run.
type Stat struct {
	Name     string        `json:"name"`
	Records  int           `json:"records"`
	Duration time.Duration `json:"duration_ms"`
	Error    string        `json:"error,omitempty"`
}

// Rejection records a value/record dropped by scope enforcement, so operators
// can see what was skipped and why (M1 hard requirement).
type Rejection struct {
	Provider string    `json:"provider"`
	Value    string    `json:"value"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
}

// Result is the output of one recon run.
type Result struct {
	Seed      string      `json:"seed"`
	Records   []Record    `json:"records"`
	Providers []Stat      `json:"providers"`
	Rejected  []Rejection `json:"rejected"`
}
