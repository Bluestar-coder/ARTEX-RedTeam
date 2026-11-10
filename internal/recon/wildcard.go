package recon

import (
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// foldWildcardRecords collapses a wildcard-DNS cluster: several subdomains that
// resolve to the same IP set AND return the same HTTP fingerprint are almost
// certainly one virtual host behind a wildcard record. Keeping every alias would
// create dozens of fake subdomains/services and multiply static analysis later.
//
// The representative is the seed itself when present, otherwise the shortest
// hostname (lexicographic tie-break). Aliases are recorded in Extra so operators
// can still see what was folded.
func foldWildcardRecords(records []Record, seed string) []Record {
	seed = strings.ToLower(strings.TrimSpace(seed))
	if seed == "" || net.ParseIP(seed) != nil {
		return records
	}
	ipsByHost := map[string][]string{}
	for _, r := range records {
		if r.Type != TypeIP {
			continue
		}
		h := strings.ToLower(strings.TrimSpace(r.Parent))
		ip := strings.TrimSpace(r.Value)
		if h == "" || ip == "" {
			continue
		}
		ipsByHost[h] = append(ipsByHost[h], ip)
	}
	for h, ips := range ipsByHost {
		sort.Strings(ips)
		ipsByHost[h] = uniqStrings(ips)
	}

	type serviceInfo struct {
		record Record
		host   string
	}
	services := map[string]serviceInfo{}
	for _, r := range records {
		if r.Type != TypeService {
			continue
		}
		host := serviceHost(r.Value)
		if host == "" {
			continue
		}
		services[host] = serviceInfo{record: r, host: host}
	}

	groups := map[string][]string{}
	for host, svc := range services {
		if host != seed && !strings.HasSuffix(host, "."+seed) {
			continue
		}
		res := svc.record
		// Empty fingerprint means "HTTP answered but we know nothing about the
		// app"; folding on that would risk merging distinct soft-404 vhosts.
		if res.Title == "" && res.FaviconMMH3 == "" {
			continue
		}
		ips := ipsByHost[host]
		if len(ips) == 0 {
			continue
		}
		key := fingerprintKey(res) + "|" + strings.Join(ips, ",")
		groups[key] = append(groups[key], host)
	}

	aliases := map[string]string{} // alias -> representative
	extraAliases := map[string][]string{}
	for _, hosts := range groups {
		if len(hosts) < 2 {
			continue
		}
		// A group with the seed keeps the seed as representative; otherwise
		// prefer the shortest (most canonical-looking) hostname.
		rep := ""
		for _, h := range hosts {
			if h == seed {
				rep = seed
				break
			}
		}
		if rep == "" {
			sort.Slice(hosts, func(i, j int) bool {
				if len(hosts[i]) != len(hosts[j]) {
					return len(hosts[i]) < len(hosts[j])
				}
				return hosts[i] < hosts[j]
			})
			rep = hosts[0]
		}
		for _, h := range hosts {
			if h == rep {
				continue
			}
			aliases[h] = rep
			extraAliases[rep] = append(extraAliases[rep], h)
		}
	}
	if len(aliases) == 0 {
		return records
	}
	for rep := range extraAliases {
		sort.Strings(extraAliases[rep])
	}

	// Keep the representative service/subdomain, drop alias duplicates, and
	// attach the folded list. If the seed itself is in the group, it already has
	// a service/subdomain record; ensure that record exists for the extra.
	repSeenService := map[string]bool{}
	out := make([]Record, 0, len(records))
	for _, r := range records {
		switch r.Type {
		case TypeService:
			h := serviceHost(r.Value)
			if rep, dropped := aliases[h]; dropped {
				_ = rep
				continue
			}
			if extraAliases[h] != nil {
				r.Extra = withWildcardAliases(r.Extra, extraAliases[h])
				repSeenService[h] = true
			}
			out = append(out, r)
		case TypeSubdomain:
			h := strings.ToLower(strings.TrimSpace(r.Value))
			if _, dropped := aliases[h]; dropped {
				continue
			}
			if extraAliases[h] != nil {
				r.Extra = withWildcardAliases(r.Extra, extraAliases[h])
			}
			out = append(out, r)
		case TypeIP:
			h := strings.ToLower(strings.TrimSpace(r.Parent))
			if rep, moved := aliases[h]; moved {
				r.Parent = rep
			}
			out = append(out, r)
		default:
			out = append(out, r)
		}
	}

	// If a representative only existed as an IP parent (no service/subdomain
	// record, e.g. the seed was folded as an alias only via IP), still surface the
	// alias list on the seed root record so it is not lost.
	for rep, list := range extraAliases {
		if repSeenService[rep] {
			continue
		}
		for i := range out {
			if out[i].Type == TypeRootDomain && strings.EqualFold(out[i].Value, rep) {
				out[i].Extra = withWildcardAliases(out[i].Extra, list)
				break
			}
		}
	}
	// Deduplicate bare IP records: wildcard aliases all resolve to the same IP.
	seenIP := map[string]bool{}
	deduped := out[:0]
	for _, r := range out {
		if r.Type == TypeIP {
			k := strings.ToLower(strings.TrimSpace(r.Value)) + "|" + strings.ToLower(strings.TrimSpace(r.Parent))
			if seenIP[k] {
				continue
			}
			seenIP[k] = true
		}
		deduped = append(deduped, r)
	}
	return deduped
}

func serviceHost(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func fingerprintKey(r Record) string {
	status := 0
	if r.StatusCode != nil {
		status = *r.StatusCode
	}
	length := int64(0)
	if r.ContentLength != nil {
		length = *r.ContentLength
	}
	return statusString(status) + "|" + r.Title + "|" + r.FaviconMMH3 + "|" + lengthString(length)
}

func statusString(v int) string {
	if v == 0 {
		return ""
	}
	return intString(v)
}

func lengthString(v int64) string {
	if v == 0 {
		return ""
	}
	return intString64(v)
}

func intString(v int) string { return strconv.Itoa(v) }

func intString64(v int64) string { return strconv.FormatInt(v, 10) }

func withWildcardAliases(extra map[string]any, aliases []string) map[string]any {
	if extra == nil {
		extra = map[string]any{}
	}
	merged := make([]string, 0, len(aliases))
	seen := map[string]bool{}
	for _, a := range aliases {
		if seen[a] {
			continue
		}
		seen[a] = true
		merged = append(merged, a)
	}
	if prev, ok := extra["wildcard_aliases"].([]string); ok {
		for _, a := range prev {
			if !seen[a] {
				seen[a] = true
				merged = append(merged, a)
			}
		}
	}
	sort.Strings(merged)
	extra["wildcard_aliases"] = merged
	return extra
}
