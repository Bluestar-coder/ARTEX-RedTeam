package recon

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Scope is the hard boundary for a recon run. Every provider checks candidates
// against it before issuing a request, and the orchestrator re-checks every
// returned record before persistence (defense in depth: "never collect then
// filter").
type Scope struct {
	Seed            string
	AllowedSuffixes []string
	Excludes        []string
}

// NewScope builds a scope from the seed plus optional allowed suffixes and
// excludes. When allowed is empty, only the seed itself (and its subdomains) is
// allowed. Entries may be domains, "*.example.com" wildcards, or IP/CIDR values;
// excludes match exact host or any subdomain of the entry.
func NewScope(seed string, allowed, excludes []string) (Scope, error) {
	seedHost, err := normalizeHost(seed)
	if err != nil {
		return Scope{}, fmt.Errorf("seed %q: %w", seed, err)
	}
	if len(allowed) == 0 {
		allowed = []string{seedHost}
	}
	out := Scope{Seed: seedHost}
	seen := map[string]struct{}{}
	for _, a := range allowed {
		h, err := normalizeHost(a)
		if err != nil {
			return Scope{}, fmt.Errorf("allowed suffix %q: %w", a, err)
		}
		h = strings.TrimPrefix(h, "*.")
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		out.AllowedSuffixes = append(out.AllowedSuffixes, h)
	}
	for _, e := range excludes {
		h, err := normalizeHost(e)
		if err != nil {
			return Scope{}, fmt.Errorf("exclude %q: %w", e, err)
		}
		out.Excludes = append(out.Excludes, strings.TrimPrefix(h, "*."))
	}
	return out, nil
}

// Allow reports whether a host/URL/IP value is inside the scope. The reason is
// populated on rejection and intended for logs/Result.Rejected.
func (s Scope) Allow(value string) (bool, string) {
	host, err := normalizeHost(value)
	if err != nil {
		return false, fmt.Sprintf("无法解析为域名/IP: %v", err)
	}
	for _, ex := range s.Excludes {
		if host == ex || strings.HasSuffix(host, "."+ex) {
			return false, "命中排除列表 " + ex
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		return s.allowIP(host)
	}
	for _, suffix := range s.AllowedSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("不在允许的域后缀 %v 内", s.AllowedSuffixes)
}

// AllowIP checks an IP that is not tied to a host (external intel can return
// bare IPs). Excludes are the only IP-level constraint in M1; IPs derived from
// an in-scope host are accepted because their parent host was checked.
func (s Scope) AllowIP(ipStr string) (bool, string) {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false, "非法 IP"
	}
	for _, ex := range s.Excludes {
		if net.ParseIP(ex) != nil && net.ParseIP(ex).Equal(ip) {
			return false, "命中排除列表 " + ex
		}
	}
	return true, ""
}

func (s Scope) allowIP(host string) (bool, string) { return s.AllowIP(host) }

// normalizeHost turns a domain/URL into a lower-case host without trailing dot.
func normalizeHost(raw string) (string, error) {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return "", fmt.Errorf("empty host")
	}
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("invalid URL")
		}
		v = u.Hostname()
	}
	v = strings.TrimSuffix(v, ".")
	v = strings.TrimPrefix(v, "*.")
	if strings.ContainsAny(v, "/ \t") {
		return "", fmt.Errorf("invalid host %q", raw)
	}
	if net.ParseIP(v) == nil && !validDomain(v) {
		return "", fmt.Errorf("invalid domain %q", raw)
	}
	return v, nil
}

func validDomain(v string) bool {
	if len(v) == 0 || len(v) > 253 || !strings.Contains(v, ".") {
		return false
	}
	for _, label := range strings.Split(v, ".") {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for _, r := range label {
			if !(r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return false
			}
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
	}
	return true
}

// HostOf extracts the normalized hostname from a URL or host value.
func HostOf(value string) string {
	h, err := normalizeHost(value)
	if err != nil {
		return ""
	}
	return h
}

// validIP reports whether s is an IP literal.
func validIP(s string) bool { return net.ParseIP(strings.TrimSpace(s)) != nil }

// normalizeServiceURL lowercases scheme/host and drops a bare trailing slash so
// deduping the same probed URL is stable.
func normalizeServiceURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String()
}
