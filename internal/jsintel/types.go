// Package jsintel statically extracts API interfaces and sensitive hints from
// front-end JavaScript. It never executes JS: everything is text/regex analysis
// with bounded input sizes. Obfuscated code is flagged for manual review, not
// deobfuscated.
package jsintel

// SourceRef points at one occurrence in one JS resource.
type SourceRef struct {
	PageURL   string `json:"page_url"`   // HTML page that referenced the script
	ScriptURL string `json:"script_url"` // JS file (or page URL for inline scripts)
	Line      int    `json:"line"`       // 1-based line in that script
	Offset    int    `json:"offset"`     // byte offset in that script
	Snippet   string `json:"snippet"`    // ±N chars of raw context
}

// Confidence ranks how much static evidence supports an extracted interface.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"   // literal path + explicit method call
	ConfidenceMedium Confidence = "medium" // one-level constant / template / axios baseURL recovery
	ConfidenceLow    Confidence = "low"    // bare path suffix or method unknown
)

// Interface is one extracted API endpoint.
type Interface struct {
	Host       string      `json:"host"`
	Method     string      `json:"method"` // GET/POST/... or "unknown"
	Path       string      `json:"path"`   // pathname only; query params are in Params
	Params     []string    `json:"params,omitempty"`
	Tags       []string    `json:"tags,omitempty"` // admin / upload / payment
	Rule       string      `json:"rule"`           // fetch / axios / xhr / jquery / path_literal / template / const_fold / axios_baseurl / config_object / bare_suffix
	Confidence Confidence  `json:"confidence"`
	Sources    []SourceRef `json:"sources"`
}

// Sensitive is one sensitive hint with enough context to avoid keyword-only noise.
type Sensitive struct {
	Host    string      `json:"host"`
	Kind    string      `json:"kind"` // hardcoded_secret / internal_address / test_env_domain / third_party_sdk / upload_hint / payment_callback / admin_path
	Rule    string      `json:"rule"`
	Value   string      `json:"value"` // matched value (secret masked when it looks like a credential)
	Sources []SourceRef `json:"sources"`
}

// Obfuscation flags code that needs manual review; no deobfuscation is attempted.
type Obfuscation struct {
	Host      string    `json:"host"`
	Reason    string    `json:"reason"` // hex_identifiers / dynamic_eval / base64_decode / path_concatenation
	ScriptURL string    `json:"script_url"`
	Source    SourceRef `json:"source"`
}

// Result is the combined extraction output for one JS resource.
type Result struct {
	Interfaces   []Interface
	Sensitives   []Sensitive
	Obfuscations []Obfuscation
}

// Limits are the M2 fetch caps (bytes). Exposed for the fetcher and tests.
const (
	DefaultMaxFileBytes = 5 << 20  // 5 MiB per JS file
	DefaultMaxHostBytes = 50 << 20 // 50 MiB per host
)

// snippetRadius is how much raw context is kept around a match.
const snippetRadius = 80
