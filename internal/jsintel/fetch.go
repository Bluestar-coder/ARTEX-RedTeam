package jsintel

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/recon"
	"golang.org/x/net/html"
)

// Target is one alive HTTP service to scan.
type Target struct {
	Host    string
	PageURL string
	AssetID int64
}

// JSFile is one scanned script resource (inline or external).
type JSFile struct {
	URL    string
	Bytes  int64
	Inline bool
}

// Skip records a resource that was not fetched/parsed, with the reason.
type Skip struct {
	URL    string
	Reason string
	Bytes  int64
}

// Aggregate is the scan output for a batch of targets. Callers persist it as
// graph facts; the package itself never touches the DB.
type Aggregate struct {
	Targets      int
	Pages        int
	JSFiles      int
	Bytes        int64
	Interfaces   []Interface
	Sensitives   []Sensitive
	Obfuscations []Obfuscation
	Skipped      []Skip
	Files        []JSFile
}

// Fetcher performs static-only collection: HTTP GET + text parsing. JavaScript is
// never executed.
type Fetcher struct {
	Client       *http.Client
	Scope        recon.Scope
	Concurrency  int
	Timeout      time.Duration
	MaxFileBytes int64 // safety cap for one page/script (default 8 MiB)
}

// NewFetcher builds a fetcher; TLS verification is skipped like the recon probe
// (targets are often self-signed in pentest engagements).
func NewFetcher(scope recon.Scope, concurrency int, timeout time.Duration) *Fetcher {
	if concurrency <= 0 {
		concurrency = 8
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Fetcher{
		Client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // pentest target
				DisableKeepAlives: true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Scope:        scope,
		Concurrency:  concurrency,
		Timeout:      timeout,
		MaxFileBytes: 8 << 20,
	}
}

// Scan visits every target page, extracts its scripts and statically analyzes
// them. Scripts whose host is out of scope are skipped before any request.
func (f *Fetcher) Scan(ctx context.Context, targets []Target) Aggregate {
	var agg Aggregate
	agg.Targets = len(targets)
	type dedupeState struct {
		ifaces map[string]*Interface
		sens   map[string]*Sensitive
		obf    map[string]bool
		files  map[string]bool
	}
	st := dedupeState{
		ifaces: map[string]*Interface{}, sens: map[string]*Sensitive{},
		obf: map[string]bool{}, files: map[string]bool{},
	}
	for _, t := range targets {
		if t.PageURL == "" || t.Host == "" {
			continue
		}
		if ok, reason := f.Scope.Allow(t.Host); !ok {
			log.Printf("[jsintel] 跳过越界页面 %s: %s", t.PageURL, reason)
			agg.Skipped = append(agg.Skipped, Skip{URL: t.PageURL, Reason: "out_of_scope: " + reason})
			continue
		}
		pageBody, err := f.get(ctx, t.PageURL)
		if err != nil {
			log.Printf("[jsintel] 页面抓取失败 %s: %v", t.PageURL, err)
			agg.Skipped = append(agg.Skipped, Skip{URL: t.PageURL, Reason: "fetch: " + err.Error()})
			continue
		}
		agg.Pages++
		agg.Bytes += int64(len(pageBody))
		scripts := parseScripts(t.PageURL, pageBody)
		type job struct {
			src    string
			inline bool
			code   string
		}
		jobs := make([]job, 0, len(scripts))
		for _, s := range scripts {
			if s.inline {
				jobs = append(jobs, job{inline: true, code: s.code})
				continue
			}
			src := s.src
			u, err := url.Parse(src)
			if err != nil || u.Host == "" {
				continue
			}
			if ok, reason := f.Scope.Allow(u.Hostname()); !ok {
				log.Printf("[jsintel] 跳过越界 JS %s: %s", src, reason)
				agg.Skipped = append(agg.Skipped, Skip{URL: src, Reason: "out_of_scope: " + reason})
				continue
			}
			jobs = append(jobs, job{src: src})
		}
		sem := make(chan struct{}, f.Concurrency)
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, jb := range jobs {
			wg.Add(1)
			go func(jb job) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
					defer func() { <-sem }()
				case <-ctx.Done():
					return
				}
				scriptURL := jb.src
				var code []byte
				if jb.inline {
					code = []byte(jb.code)
					scriptURL = t.PageURL
				} else {
					var err error
					code, err = f.get(ctx, jb.src)
					if err != nil {
						log.Printf("[jsintel] JS 抓取失败 %s: %v", jb.src, err)
						mu.Lock()
						agg.Skipped = append(agg.Skipped, Skip{URL: jb.src, Reason: "fetch: " + err.Error()})
						mu.Unlock()
						return
					}
				}
				res := Extract(t.Host, t.PageURL, scriptURL, string(code))
				mu.Lock()
				defer mu.Unlock()
				agg.Bytes += int64(len(code))
				agg.JSFiles++
				if !jb.inline {
					if !st.files[jb.src] {
						st.files[jb.src] = true
						agg.Files = append(agg.Files, JSFile{URL: jb.src, Bytes: int64(len(code))})
					}
				}
				for _, it := range res.Interfaces {
					key := it.Host + "|" + it.Method + "|" + strings.ToLower(it.Path)
					if cur, ok := st.ifaces[key]; ok {
						cur.Params = mergeStrings(cur.Params, it.Params)
						cur.Tags = mergeStrings(cur.Tags, it.Tags)
						cur.Sources = appendSources(cur.Sources, it.Sources...)
					} else {
						cp := it
						st.ifaces[key] = &cp
					}
				}
				for _, s := range res.Sensitives {
					key := s.Host + "|" + s.Kind + "|" + s.Rule + "|" + s.Value
					if cur, ok := st.sens[key]; ok {
						cur.Sources = appendSources(cur.Sources, s.Sources...)
					} else {
						cp := s
						st.sens[key] = &cp
					}
				}
				for _, o := range res.Obfuscations {
					key := o.ScriptURL + "|" + o.Reason
					if !st.obf[key] {
						st.obf[key] = true
						agg.Obfuscations = append(agg.Obfuscations, o)
					}
				}
			}(jb)
		}
		wg.Wait()
	}
	for _, it := range st.ifaces {
		agg.Interfaces = append(agg.Interfaces, *it)
	}
	for _, s := range st.sens {
		agg.Sensitives = append(agg.Sensitives, *s)
	}
	sort.Slice(agg.Interfaces, func(i, j int) bool {
		if agg.Interfaces[i].Host != agg.Interfaces[j].Host {
			return agg.Interfaces[i].Host < agg.Interfaces[j].Host
		}
		return agg.Interfaces[i].Path < agg.Interfaces[j].Path
	})
	sort.Slice(agg.Sensitives, func(i, j int) bool {
		if agg.Sensitives[i].Kind != agg.Sensitives[j].Kind {
			return agg.Sensitives[i].Kind < agg.Sensitives[j].Kind
		}
		return agg.Sensitives[i].Value < agg.Sensitives[j].Value
	})
	return agg
}

func (f *Fetcher) get(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, f.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-redteam-jsintel/1.0")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	limit := f.MaxFileBytes
	if limit <= 0 {
		limit = 8 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("resource exceeds %d bytes", limit)
	}
	return body, nil
}

type scriptTag struct {
	src    string
	inline bool
	code   string
}

// parseScripts extracts external script URLs and inline script bodies. Non-JS
// script types (JSON-LD, templates) are ignored.
func parseScripts(pageURL string, body []byte) []scriptTag {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil
	}
	base, _ := url.Parse(pageURL)
	var out []scriptTag
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			var src, typ string
			for _, a := range n.Attr {
				switch strings.ToLower(a.Key) {
				case "src":
					src = strings.TrimSpace(a.Val)
				case "type":
					typ = strings.ToLower(strings.TrimSpace(a.Val))
				}
			}
			if typ != "" && typ != "text/javascript" && typ != "application/javascript" && typ != "module" {
				return
			}
			if src != "" {
				if u, err := url.Parse(src); err == nil && base != nil {
					out = append(out, scriptTag{src: base.ResolveReference(u).String()})
				}
				return
			}
			var b strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					b.WriteString(c.Data)
				}
			}
			if strings.TrimSpace(b.String()) != "" {
				out = append(out, scriptTag{inline: true, code: b.String()})
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func mergeStrings(a, b []string) []string { return uniq(append(a, b...)) }

func appendSources(dst []SourceRef, refs ...SourceRef) []SourceRef {
	for _, r := range refs {
		dst = appendSource(dst, r)
	}
	return dst
}
