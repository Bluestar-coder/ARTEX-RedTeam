package recon

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Run executes every enabled provider independently, re-checks scope on every
// returned record, deduplicates by (type, value) and returns the merged result.
// A provider error or panic degrades that provider only; the run keeps going.
func Run(ctx context.Context, reg *Registry, seed string, scope Scope, cfg Config) Result {
	seedHost := HostOf(seed)
	if seedHost == "" {
		seedHost = strings.TrimSpace(seed)
	}
	res := Result{Seed: seedHost, Records: []Record{}, Providers: []Stat{}, Rejected: []Rejection{}}
	if reg == nil {
		return res
	}
	now := time.Now().UTC()
	type keyed struct {
		rec Record
		key string
	}
	byKey := map[string]Record{}
	order := []string{}

	for _, p := range reg.Providers() {
		started := time.Now()
		req := Request{Seed: seedHost, Scope: scope, Now: now}
		runCtx := ctx
		var cancel context.CancelFunc
		if cfg.ProviderTimeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, cfg.ProviderTimeout)
		}
		recs, err := safeRun(runCtx, p, req)
		if cancel != nil {
			cancel()
		}
		stat := Stat{Name: p.Name(), Records: len(recs), Duration: time.Since(started)}
		if err != nil {
			stat.Error = err.Error()
			logProviderf(p.Name(), "运行失败（降级，不影响其它源）: %v", err)
		}
		for _, raw := range recs {
			rec, reason := normalizeRecord(raw, p.Name(), now)
			if reason != "" {
				res.reject(p.Name(), raw.Value, reason, now)
				continue
			}
			ok, reason := recordInScope(scope, rec)
			if !ok {
				res.reject(p.Name(), rec.Value, reason, now)
				continue
			}
			key := dedupeKey(rec)
			if prev, ok := byKey[key]; ok {
				byKey[key] = mergeRecord(prev, rec)
			} else {
				byKey[key] = rec
				order = append(order, key)
			}
		}
		res.Providers = append(res.Providers, stat)
	}

	sort.Strings(order)
	for _, k := range order {
		res.Records = append(res.Records, byKey[k])
	}
	sort.Slice(res.Records, func(i, j int) bool {
		if res.Records[i].Type != res.Records[j].Type {
			return res.Records[i].Type < res.Records[j].Type
		}
		return res.Records[i].Value < res.Records[j].Value
	})
	sort.Slice(res.Rejected, func(i, j int) bool {
		if res.Rejected[i].Provider != res.Rejected[j].Provider {
			return res.Rejected[i].Provider < res.Rejected[j].Provider
		}
		return res.Rejected[i].Value < res.Rejected[j].Value
	})
	return res
}

func safeRun(ctx context.Context, p Provider, req Request) (recs []Record, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("provider panic: %v", r)
			logProviderf(p.Name(), "panic: %v", r)
		}
	}()
	return p.Run(ctx, req)
}

func (r *Result) reject(provider, value, reason string, at time.Time) {
	rej := Rejection{Provider: provider, Value: value, Reason: reason, At: at.UTC()}
	r.Rejected = append(r.Rejected, rej)
	// Log every rejection: M1 requires "谁、什么时候、因为什么被丢".
	log.Printf("[recon] 丢弃越界记录 provider=%s value=%q reason=%s at=%s", provider, value, reason, at.UTC().Format(time.RFC3339))
}

// normalizeRecord validates and canonicalizes a provider record.
func normalizeRecord(rec Record, provider string, now time.Time) (Record, string) {
	if rec.Source == "" {
		rec.Source = provider
	}
	if rec.ObservedAt.IsZero() {
		rec.ObservedAt = now
	}
	rec.ObservedAt = rec.ObservedAt.UTC()
	switch rec.Type {
	case TypeRootDomain, TypeSubdomain:
		h := HostOf(rec.Value)
		if h == "" {
			return rec, "invalid domain value"
		}
		rec.Value = h
		if rec.Parent == "" {
			rec.Parent = h
		} else {
			rec.Parent = HostOf(rec.Parent)
		}
	case TypeService:
		h := HostOf(rec.Value)
		if h == "" {
			return rec, "invalid service URL"
		}
		rec.Value = normalizeServiceURL(rec.Value)
		if rec.Parent == "" {
			rec.Parent = h
		} else {
			rec.Parent = HostOf(rec.Parent)
		}
	case TypeInterface:
		u, err := url.Parse(rec.Value)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return rec, "invalid interface URL"
		}
		rec.Parent = u.Hostname()
		rec.Value = u.Scheme + "://" + u.Hostname() + u.Path
	case TypeIP:
		ip := strings.TrimSpace(rec.Value)
		if !validIP(ip) {
			return rec, "invalid IP value"
		}
		rec.Value = ip
		if rec.Parent != "" {
			rec.Parent = HostOf(rec.Parent)
		}
	default:
		return rec, "unknown record type " + string(rec.Type)
	}
	rec.Technologies = uniqStrings(rec.Technologies)
	sort.Strings(rec.Technologies)
	return rec, ""
}

func recordInScope(scope Scope, rec Record) (bool, string) {
	switch rec.Type {
	case TypeRootDomain, TypeSubdomain, TypeService, TypeInterface:
		return scope.Allow(rec.Value)
	case TypeIP:
		if rec.Parent != "" {
			if ok, reason := scope.Allow(rec.Parent); !ok {
				return false, "上级主机越界: " + reason
			}
		}
		return scope.AllowIP(rec.Value)
	}
	return false, "unknown record type"
}

func dedupeKey(rec Record) string {
	return string(rec.Type) + "|" + strings.ToLower(rec.Value)
}

func mergeRecord(a, b Record) Record {
	// Prefer the newer observation, but keep non-empty facts from either source.
	out := a
	if b.ObservedAt.After(a.ObservedAt) {
		out = b
	}
	if out.Title == "" {
		out.Title = firstNonEmpty(a.Title, b.Title)
	}
	if out.FaviconMMH3 == "" {
		out.FaviconMMH3 = firstNonEmpty(a.FaviconMMH3, b.FaviconMMH3)
	}
	if out.StatusCode == nil {
		out.StatusCode = firstInt(a.StatusCode, b.StatusCode)
	}
	if out.ContentLength == nil {
		out.ContentLength = firstInt64(a.ContentLength, b.ContentLength)
	}
	if out.IP == "" {
		out.IP = firstNonEmpty(a.IP, b.IP)
	}
	out.Technologies = uniqStrings(append(append([]string{}, a.Technologies...), b.Technologies...))
	if out.RawRef == "" {
		out.RawRef = firstNonEmpty(a.RawRef, b.RawRef)
	}
	out.Source = firstNonEmpty(b.Source, a.Source)
	if out.Extra == nil {
		out.Extra = map[string]any{}
	}
	// Track every provider that observed this asset (M3 evidence lineage).
	seen := map[string]bool{}
	var sources []string
	for _, s := range []string{a.Source, b.Source} {
		if s != "" && !seen[s] {
			seen[s] = true
			sources = append(sources, s)
		}
	}
	if existing, ok := out.Extra["sources"].([]string); ok {
		for _, s := range existing {
			if !seen[s] {
				seen[s] = true
				sources = append(sources, s)
			}
		}
	}
	sort.Strings(sources)
	if len(sources) > 0 {
		out.Extra["sources"] = sources
	}
	return out
}

func uniqStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstInt(vals ...*int) *int {
	for _, v := range vals {
		if v != nil {
			n := *v
			return &n
		}
	}
	return nil
}

func firstInt64(vals ...*int64) *int64 {
	for _, v := range vals {
		if v != nil {
			n := *v
			return &n
		}
	}
	return nil
}
