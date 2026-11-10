package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/jsintel"
)

// mergedStaticInterface is one API path after host aliases have been folded.
type mergedStaticInterface struct {
	itf   jsintel.Interface
	hosts []string
}

// mergeStaticInterfacesByPath collapses the same method+path observed on
// multiple host aliases into one interface. Wildcard DNS can serve the same SPA
// from dozens of names; storing one fact per host multiplies both the graph and
// the planner's attention by the number of aliases without adding information.
// The representative host prefers the operator-supplied domain, then the
// shortest/lexicographic host; every hit is kept in hosts.
func mergeStaticInterfacesByPath(ifaces []jsintel.Interface, domain string) []mergedStaticInterface {
	domain = strings.ToLower(strings.TrimSpace(domain))
	byKey := map[string]*mergedStaticInterface{}
	order := []string{}
	for _, itf := range ifaces {
		if itf.Path == "" || itf.Host == "" {
			continue
		}
		key := itf.Method + "|" + strings.ToLower(itf.Path)
		host := strings.ToLower(itf.Host)
		cur, ok := byKey[key]
		if !ok {
			cp := itf
			cp.Host = host
			cur = &mergedStaticInterface{itf: cp, hosts: []string{host}}
			byKey[key] = cur
			order = append(order, key)
			continue
		}
		cur.itf.Params = unionStrings(cur.itf.Params, itf.Params)
		cur.itf.Tags = unionStrings(cur.itf.Tags, itf.Tags)
		cur.itf.Sources = unionSources(cur.itf.Sources, itf.Sources)
		if jsintel.ConfidenceRank(itf.Confidence) > jsintel.ConfidenceRank(cur.itf.Confidence) {
			cur.itf.Confidence = itf.Confidence
			cur.itf.Rule = itf.Rule
		}
		if !containsString(cur.hosts, host) {
			cur.hosts = append(cur.hosts, host)
		}
	}
	out := make([]mergedStaticInterface, 0, len(order))
	for _, key := range order {
		m := byKey[key]
		sort.Strings(m.hosts)
		m.itf.Host = representativeHost(m.hosts, domain)
		out = append(out, *m)
	}
	return out
}

func representativeHost(hosts []string, domain string) string {
	if len(hosts) == 0 {
		return ""
	}
	if domain != "" {
		for _, h := range hosts {
			if h == domain {
				return h
			}
		}
		// Path merged from alias-only hosts (e.g. wildcard DNS): anchor the fact
		// on the operator-supplied domain so dynamic facts on that host and the
		// planner's view still line up.
		for _, h := range hosts {
			if strings.HasSuffix(h, "."+domain) {
				return domain
			}
		}
	}
	sort.Slice(hosts, func(i, j int) bool {
		if len(hosts[i]) != len(hosts[j]) {
			return len(hosts[i]) < len(hosts[j])
		}
		return hosts[i] < hosts[j]
	})
	return hosts[0]
}

func unionStrings(a, b []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for _, v := range append(append([]string{}, a...), b...) {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func unionSources(a, b []jsintel.SourceRef) []jsintel.SourceRef {
	seen := map[string]bool{}
	out := make([]jsintel.SourceRef, 0, len(a)+len(b))
	for _, ref := range append(append([]jsintel.SourceRef{}, a...), b...) {
		key := fmt.Sprintf("%s|%s|%d|%s", ref.PageURL, ref.ScriptURL, ref.Line, ref.Snippet)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	return out
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
