package jsintel

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

var reScriptSrc = regexp.MustCompile(`(?is)<script[^>]*\bsrc=["\']([^"\']+)["\']`)

// TestParserBenchmark is the AST-parser selection benchmark. Skipped unless
// JS_PARSE_BENCH=1; run in the verification workflow and capture PARSEBENCH
// lines. It fetches the largest same-host script (<=5MiB) per site, parses it
// three times, and reports wall time + heap/total allocation deltas.
func TestParserBenchmark(t *testing.T) {
	if os.Getenv("JS_PARSE_BENCH") != "1" {
		t.Skip("set JS_PARSE_BENCH=1 to run")
	}
	rawSites := os.Getenv("JS_PARSE_BENCH_SITES")
	if strings.TrimSpace(rawSites) == "" {
		rawSites = "https://www.figma.com/,https://www.cloudflare.com/,https://www.python.org/,https://vercel.com/"
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if direct := strings.TrimSpace(os.Getenv("JS_PARSE_BENCH_URL")); direct != "" {
		code, err := fetchScript(client, direct)
		host := ""
		if u, e := url.Parse(direct); e == nil {
			host = u.Hostname()
		}
		site := "direct:" + host
		if err != nil {
			t.Logf("PARSEBENCH site=%s error=%q", site, err)
			return
		}
		benchPayload(t, site, "real_bundle", code)
		benchPayload(t, site, "synthetic_5MiB", syntheticBundle())
		return
	}
	sites := strings.Split(rawSites, ",")
	for _, raw := range sites {
		site := strings.TrimSpace(raw)
		if site == "" {
			continue
		}
		code, src, err := largestScript(client, site)
		if err != nil {
			t.Logf("PARSEBENCH site=%s error=%q\n", site, err)
			continue
		}
		benchPayload(t, site, "real_largest_script", code)
		if len(code) > 0 {
			t.Logf("PARSEBENCH real bundle site=%s src=%s bytes=%d", site, src, len(code))
		}
		// Synthetic ~5MiB single-array bundle: upper-bound check for the per-file cap.
		synth := []byte(`const DATA=["` + strings.Repeat("x", (5<<20)-64) + `"]; fetch("/api/"+DATA.length);`)
		benchPayload(t, site, "synthetic_5MiB", synth)
	}
}

func syntheticBundle() []byte {
	return []byte(`const DATA=["` + strings.Repeat("x", (5<<20)-64) + `"]; fetch("/api/"+DATA.length);`)
}

func fetchScript(client *http.Client, raw string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, raw, nil)
	req.Header.Set("User-Agent", "artex-redteam-jsintel-bench/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, DefaultMaxFileBytes+1))
}

func benchPayload(t *testing.T, site, label string, payload []byte) {
	t.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var best time.Duration
	runs := 3
	var parseErr error
	for i := 0; i < runs; i++ {
		start := time.Now()
		ast, err := js.Parse(parse.NewInput(bytes.NewReader(payload)), js.Options{})
		d := time.Since(start)
		parseErr = err
		if ast == nil || err != nil {
			break
		}
		if i == 0 || d < best {
			best = d
		}
	}
	runtime.ReadMemStats(&after)
	t.Logf("PARSEBENCH site=%s label=%s bytes=%d parse_ms=%d heap_delta_mb=%d total_alloc_delta_mb=%d err=%v",
		site, label, len(payload), best.Milliseconds(),
		int64(after.HeapAlloc-before.HeapAlloc)>>20, int64(after.TotalAlloc-before.TotalAlloc)>>20, parseErr)
}

func largestScript(client *http.Client, site string) ([]byte, string, error) {
	req, _ := http.NewRequest(http.MethodGet, site, nil)
	req.Header.Set("User-Agent", "artex-redteam-jsintel-bench/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	// Fetch candidates and keep the largest <=5MiB.
	var best []byte
	var bestSrc string
	for _, raw := range scriptSrcs(page, site) {
		r, _ := http.NewRequest(http.MethodGet, raw, nil)
		r.Header.Set("User-Agent", "artex-redteam-jsintel-bench/1.0")
		rr, err := client.Do(r)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rr.Body, DefaultMaxFileBytes+1))
		rr.Body.Close()
		if rr.StatusCode != 200 || int64(len(b)) > DefaultMaxFileBytes {
			continue
		}
		if len(b) > len(best) {
			best = b
			bestSrc = raw
		}
	}
	if len(best) == 0 {
		return nil, "", fmt.Errorf("no same-host script <=5MiB")
	}
	return best, bestSrc, nil
}

func scriptSrcs(page []byte, site string) []string {
	base, _ := url.Parse(site)
	host := base.Hostname()
	seen := map[string]bool{}
	var out []string
	for _, m := range reScriptSrc.FindAllStringSubmatch(string(page), -1) {
		u, err := url.Parse(m[1])
		if err != nil {
			continue
		}
		abs := base.ResolveReference(u)
		s := abs.String()
		if abs.Hostname() == host && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
