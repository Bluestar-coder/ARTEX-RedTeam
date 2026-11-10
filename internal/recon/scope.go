package recon

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Scope is the hard boundary for a recon run. Every provider checks candidates
// against it before issuing a request, and the orchestrator re-checks every
// returned record before persistence (defense in depth: "never collect then
// filter").
type Scope struct {
	Seed            string
	SeedPort        int    // target port from host:port / URL; 0 means default per scheme
	SeedScheme      string // explicit scheme from a URL seed; empty means probe https then http
	AllowedSuffixes []string
	AllowedIPs      []string // IP literals explicitly allowed (seed IP or allowed IP entries)
	Excludes        []string
}

// NewScope builds a scope from the seed plus optional allowed suffixes and
// excludes. When allowed is empty, only the seed itself (and its subdomains) is
// allowed. Entries may be domains, "*.example.com" wildcards, or IP/CIDR values;
// excludes match exact host or any subdomain of the entry.
func NewScope(seed string, allowed, excludes []string) (Scope, error) {
	spec, err := parseTargetHost(seed)
	if err != nil {
		return Scope{}, fmt.Errorf("seed %q: %w", seed, err)
	}
	if len(allowed) == 0 {
		allowed = []string{spec.Host}
	}
	out := Scope{Seed: spec.Host, SeedPort: spec.Port, SeedScheme: spec.Scheme}
	seen := map[string]struct{}{}
	addAllowed := func(raw string, isSeed bool) error {
		h, err := normalizeHost(raw)
		if err != nil {
			return err
		}
		if _, ok := seen[h]; ok {
			return nil
		}
		seen[h] = struct{}{}
		if net.ParseIP(h) != nil {
			out.AllowedIPs = append(out.AllowedIPs, h)
			if isSeed {
				// The seed itself is always an exact allowed IP; it is not a
				// domain suffix.
				return nil
			}
		}
		out.AllowedSuffixes = append(out.AllowedSuffixes, h)
		return nil
	}
	for _, a := range allowed {
		if err := addAllowed(a, strings.TrimSpace(strings.ToLower(a)) == spec.Host); err != nil {
			return Scope{}, fmt.Errorf("allowed suffix %q: %w", a, err)
		}
	}
	if net.ParseIP(spec.Host) != nil {
		out.AllowedIPs = append(out.AllowedIPs, spec.Host)
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
		return false, fmt.Sprintf("无法识别主机 %q: %v", value, err)
	}
	for _, ex := range s.Excludes {
		if host == ex || strings.HasSuffix(host, "."+ex) {
			return false, "命中排除列表 " + ex
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		if len(s.AllowedIPs) > 0 {
			for _, a := range s.AllowedIPs {
				if parsed := net.ParseIP(a); parsed != nil && parsed.Equal(ip) {
					return true, ""
				}
			}
			return false, fmt.Sprintf("IP %s 不在允许的 IP 范围内", host)
		}
		return false, fmt.Sprintf("IP %s 不在允许的域后缀 %v 内", host, s.AllowedSuffixes)
	}
	for _, suffix := range s.AllowedSuffixes {
		if net.ParseIP(suffix) != nil {
			continue
		}
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

// normalizeHost turns a domain/URL/host:port into a lower-case host. It keeps
// the existing Scope call sites port-agnostic; NewScope uses parseTargetHost when
// it also needs the seed port/scheme.
func normalizeHost(raw string) (string, error) {
	spec, err := parseTargetHost(raw)
	if err != nil {
		return "", err
	}
	return spec.Host, nil
}

type targetHost struct {
	Host   string
	Port   int
	Scheme string
}

// parseTargetHost accepts what the recon entry points actually receive: a bare
// domain/IP, host:port, [IPv6]:port, or URL. Single-label names such as
// "localhost" are legal host identifiers; resolution is a later, network step.
func parseTargetHost(raw string) (targetHost, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return targetHost{}, fmt.Errorf("empty host")
	}
	out := targetHost{}
	portStr := ""
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" {
			return targetHost{}, fmt.Errorf("invalid URL %q", raw)
		}
		out.Scheme = strings.ToLower(u.Scheme)
		switch out.Scheme {
		case "http", "https":
		default:
			return targetHost{}, fmt.Errorf("invalid URL scheme %q (want http or https)", u.Scheme)
		}
		out.Host = u.Hostname()
		portStr = u.Port()
	} else {
		if strings.ContainsAny(v, "/ \t\r\n") {
			return targetHost{}, fmt.Errorf("invalid host format %q", raw)
		}
		if strings.HasPrefix(v, "[") {
			h, p, err := net.SplitHostPort(v)
			if err != nil {
				// [::1] without a port is still a legal IPv6 host literal.
				if strings.HasSuffix(v, "]") && net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")) != nil {
					out.Host = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
				} else {
					return targetHost{}, fmt.Errorf("invalid IPv6 host %q", raw)
				}
			} else {
				out.Host, portStr = h, p
			}
		} else if h, p, err := net.SplitHostPort(v); err == nil {
			out.Host, portStr = h, p
		} else if strings.Count(v, ":") == 1 {
			// host:port with a non-numeric/empty port.
			return targetHost{}, fmt.Errorf("invalid port in %q", raw)
		} else {
			out.Host = v
		}
	}
	out.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(out.Host)), ".")
	out.Host = strings.TrimPrefix(out.Host, "*.")
	if out.Host == "" {
		return targetHost{}, fmt.Errorf("empty host")
	}
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			return targetHost{}, fmt.Errorf("invalid port %q (want 1-65535)", portStr)
		}
		out.Port = p
	}
	if net.ParseIP(out.Host) == nil {
		if looksLikeIPv4(out.Host) {
			return targetHost{}, fmt.Errorf("invalid IPv4 literal %q", out.Host)
		}
		if !validHostname(out.Host) {
			return targetHost{}, fmt.Errorf("invalid host format %q", raw)
		}
	}
	return out, nil
}

func looksLikeIPv4(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// validHostname accepts DNS labels plus single-label hostnames (localhost,
// internal service names). Numeric-only multi-label values are handled above as
// malformed IPv4 instead of being silently treated as DNS.
func validHostname(v string) bool {
	if len(v) == 0 || len(v) > 253 {
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
