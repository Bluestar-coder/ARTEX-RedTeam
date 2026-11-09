package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CTName is one name observed in certificate transparency logs, with the first
// issuance time when the backend exposes it.
type CTName struct {
	Name      string
	FirstSeen time.Time
	RawRef    string
}

// CTBackend is a swappable certificate-transparency query source.
type CTBackend interface {
	Name() string
	Lookup(ctx context.Context, seed string) ([]CTName, error)
}

// CTBackendDefs returns backends in configured preference order. A failure on
// one backend is logged and the next is tried; when all fail the active provider
// falls back to its built-in dictionary (M1 behavior, unchanged).
func CTBackendDefs(cfg Config, client *http.Client) []CTBackend {
	order := cfg.CTBackends
	if len(order) == 0 {
		order = strings.Split(defaultCTBackends, ",")
	}
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	out := make([]CTBackend, 0, len(order))
	seen := map[string]bool{}
	for _, raw := range order {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		switch name {
		case "crt.sh":
			out = append(out, &crtShBackend{baseURL: cfg.CTURL, client: client})
		case "crt.name":
			out = append(out, &crtNameBackend{baseURL: cfg.CTNameURL, client: client})
		case "certspotter":
			out = append(out, &certSpotterBackend{baseURL: cfg.CertSpotterURL, token: cfg.CertSpotterToken, client: client})
		default:
			// Unknown names are a config mistake; keep going so one typo doesn't
			// disable enumeration entirely.
			logProviderf(ProviderActive, "未知 CT 后端 %q（已跳过）", name)
		}
	}
	return out
}

// --- crt.sh ---------------------------------------------------------------

type crtShBackend struct {
	baseURL string
	client  *http.Client
}

func (b *crtShBackend) Name() string { return "crt.sh" }

func (b *crtShBackend) Lookup(ctx context.Context, seed string) ([]CTName, error) {
	base := strings.TrimRight(b.baseURL, "/")
	if base == "" {
		base = "https://crt.sh"
	}
	u := base + "/?q=%25." + url.QueryEscape(seed) + "&output=json"
	body, err := ctGet(ctx, b.client, u)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		NameValue  string `json:"name_value"`
		CommonName string `json:"common_name"`
		NotBefore  string `json:"not_before"`
		ID         int64  `json:"id"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("crt.sh JSON: %w", err)
	}
	var out []CTName
	for _, r := range rows {
		first := parseCTTime(r.NotBefore)
		for _, part := range strings.Split(r.NameValue+"\n"+r.CommonName, "\n") {
			if h := HostOf(part); h != "" {
				out = append(out, CTName{Name: h, FirstSeen: first, RawRef: fmt.Sprintf("crt.sh:%d", r.ID)})
			}
		}
	}
	return out, nil
}

// --- crt.name -------------------------------------------------------------

type crtNameBackend struct {
	baseURL string
	client  *http.Client
}

func (b *crtNameBackend) Name() string { return "crt.name" }

func (b *crtNameBackend) Lookup(ctx context.Context, seed string) ([]CTName, error) {
	base := strings.TrimRight(b.baseURL, "/")
	if base == "" {
		base = "https://crt.name/v1/search"
	}
	u := base + "?apex=" + url.QueryEscape(seed)
	body, err := ctGet(ctx, b.client, u)
	if err != nil {
		return nil, err
	}
	return parseCRTNameBody(body), nil
}

// parseCRTNameBody accepts either JSON (array/object/strings) or the plain-text
// response used by the crt.name CLI scripts (first whitespace field is the name).
func parseCRTNameBody(body []byte) []CTName {
	var out []CTName
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil
	}
	if trimmed[0] == '[' || trimmed[0] == '{' {
		var v any
		if json.Unmarshal(body, &v) == nil {
			walkCTJSON(v, &out)
			return out
		}
	}
	for _, line := range strings.Split(trimmed, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		h := HostOf(fields[0])
		if h == "" {
			continue
		}
		first := time.Time{}
		if len(fields) > 1 {
			first = parseCTTime(fields[1])
		}
		out = append(out, CTName{Name: h, FirstSeen: first, RawRef: "crt.name:" + h})
	}
	return out
}

func walkCTJSON(v any, out *[]CTName) {
	switch x := v.(type) {
	case string:
		for _, token := range strings.Fields(x) {
			if h := HostOf(token); h != "" {
				*out = append(*out, CTName{Name: h, RawRef: "crt.name:" + h})
			}
		}
	case []any:
		for _, item := range x {
			walkCTJSON(item, out)
		}
	case map[string]any:
		first := time.Time{}
		for _, key := range []string{"not_before", "first_seen", "firstSeen", "created_at"} {
			if s, ok := x[key].(string); ok {
				first = parseCTTime(s)
				break
			}
		}
		for _, key := range []string{"name_value", "common_name", "domain", "name", "host"} {
			if s, ok := x[key].(string); ok {
				if h := HostOf(s); h != "" {
					*out = append(*out, CTName{Name: h, FirstSeen: first, RawRef: "crt.name:" + h})
				}
			}
		}
		if names, ok := x["dns_names"].([]any); ok {
			for _, item := range names {
				if s, ok := item.(string); ok {
					if h := HostOf(s); h != "" {
						*out = append(*out, CTName{Name: h, FirstSeen: first, RawRef: "crt.name:" + h})
					}
				}
			}
		}
		for _, key := range []string{"results", "data", "domains"} {
			if nested, ok := x[key]; ok {
				walkCTJSON(nested, out)
			}
		}
	}
}

// --- Cert Spotter (SSLMate) ----------------------------------------------

type certSpotterBackend struct {
	baseURL string
	token   string
	client  *http.Client
}

func (b *certSpotterBackend) Name() string { return "certspotter" }

func (b *certSpotterBackend) Lookup(ctx context.Context, seed string) ([]CTName, error) {
	base := strings.TrimRight(b.baseURL, "/")
	if base == "" {
		base = "https://api.certspotter.com/v1/issuances"
	}
	u := base + "?domain=" + url.QueryEscape(seed) + "&include_subdomains=true&expand=dns_names"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-redteam-recon/1.0")
	req.Header.Set("Accept", "application/json")
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("certspotter HTTP %d: %s", resp.StatusCode, truncate(string(body), 160))
	}
	var rows []struct {
		ID        string   `json:"id"`
		NotBefore string   `json:"not_before"`
		DNSNames  []string `json:"dns_names"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("certspotter JSON: %w", err)
	}
	var out []CTName
	for _, r := range rows {
		first := parseCTTime(r.NotBefore)
		for _, name := range r.DNSNames {
			if h := HostOf(name); h != "" {
				out = append(out, CTName{Name: h, FirstSeen: first, RawRef: "certspotter:" + r.ID})
			}
		}
	}
	return out, nil
}

// --- shared helpers -------------------------------------------------------

func ctGet(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid CT URL")
	}
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("CT 查询必须使用 HTTPS（%s）", u.Scheme)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-redteam-recon/1.0")
	req.Header.Set("Accept", "application/json, text/plain;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s HTTP %d: %s", u.Host, resp.StatusCode, truncate(string(body), 160))
	}
	return body, nil
}

func parseCTTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ctBackendURLIsLoopback is used by tests to allow httptest servers.
func ctBackendURLIsLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

var _ = ctBackendURLIsLoopback
