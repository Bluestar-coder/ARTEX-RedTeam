package recon

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"html"
	"math/bits"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// techSignatures is intentionally small and deterministic: M1 only needs a
// useful first-pass fingerprint, not a full Wappalyzer clone. Matching is
// case-insensitive; each entry contributes one technology label.
type techSignature struct {
	name    string
	headers []string // "header:substring"
	body    []string // substring in body
}

var techSignatures = []techSignature{
	{name: "nginx", headers: []string{"server:nginx"}},
	{name: "Apache", headers: []string{"server:apache"}},
	{name: "IIS", headers: []string{"server:microsoft-iis"}},
	{name: "OpenResty", headers: []string{"server:openresty"}},
	{name: "Caddy", headers: []string{"server:caddy"}},
	{name: "Cloudflare", headers: []string{"server:cloudflare", "cf-ray:"}},
	{name: "Tomcat", headers: []string{"server:tomcat"}},
	{name: "PHP", headers: []string{"x-powered-by:php", "set-cookie:phpsessid"}},
	{name: "ASP.NET", headers: []string{"x-powered-by:asp.net", "set-cookie:asp.net_sessionid", "x-aspnet-version:"}},
	{name: "Express", headers: []string{"x-powered-by:express"}},
	{name: "Next.js", headers: []string{"x-powered-by:next.js"}, body: []string{"__next_data__", "/_next/static/"}},
	{name: "Java", headers: []string{"set-cookie:jsessionid"}},
	{name: "Node.js", headers: []string{"x-powered-by:node"}},
	{name: "WordPress", body: []string{"wp-content/", "wp-includes/", `name="generator" content="wordpress`}},
	{name: "jQuery", body: []string{"jquery.min.js", "jquery.js"}},
	{name: "React", body: []string{"data-reactroot", "__react", "react-dom"}},
	{name: "Vue", body: []string{"data-v-", "__vue__", "vue.js"}},
	{name: "Nuxt", body: []string{"__nuxt", "/_nuxt/"}},
	{name: "Bootstrap", body: []string{"bootstrap.min.css", "bootstrap.css"}},
	{name: "Element UI", body: []string{"element-ui", "el-button"}},
	{name: "Shiro", headers: []string{"set-cookie:rememberme=deleteMe"}},
}

func detectTechnologies(h http.Header, body []byte) []string {
	lowerBody := strings.ToLower(string(body))
	seen := map[string]bool{}
	var out []string
	for _, sig := range techSignatures {
		matched := false
		for _, hv := range sig.headers {
			parts := strings.SplitN(hv, ":", 2)
			if len(parts) != 2 {
				continue
			}
			val := strings.ToLower(h.Get(parts[0]))
			if val != "" && strings.Contains(val, parts[1]) {
				matched = true
				break
			}
		}
		if !matched {
			for _, needle := range sig.body {
				if strings.Contains(lowerBody, needle) {
					matched = true
					break
				}
			}
		}
		if matched && !seen[sig.name] {
			seen[sig.name] = true
			out = append(out, sig.name)
		}
	}
	sort.Strings(out)
	return out
}

// faviconMMH3 returns the Shodan-compatible favicon hash: mmh3 of the
// base64-encoded bytes (base64.encodebytes style with newlines every 76 chars)
// rendered as a signed 32-bit integer.
func faviconMMH3(data []byte) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString(enc[i:end])
		b.WriteByte('\n')
	}
	h := murmur3_32([]byte(b.String()), 0)
	return itoaSigned(int32(h))
}

func itoaSigned(v int32) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	var u uint32
	if neg {
		u = uint32(-int64(v))
	} else {
		u = uint32(v)
	}
	var buf [12]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// murmur3_32 is the x86 32-bit MurmurHash3 variant used by mmh3.
func murmur3_32(data []byte, seed uint32) uint32 {
	const (
		c1 = 0xcc9e2d51
		c2 = 0x1b873593
	)
	h := seed
	n := len(data)
	i := 0
	for ; i+4 <= n; i += 4 {
		k := binary.LittleEndian.Uint32(data[i:])
		k *= c1
		k = bits.RotateLeft32(k, 15)
		k *= c2
		h ^= k
		h = bits.RotateLeft32(h, 13)
		h = h*5 + 0xe6546b64
	}
	var k uint32
	switch n & 3 {
	case 3:
		k ^= uint32(data[i+2]) << 16
		fallthrough
	case 2:
		k ^= uint32(data[i+1]) << 8
		fallthrough
	case 1:
		k ^= uint32(data[i])
		k *= c1
		k = bits.RotateLeft32(k, 15)
		k *= c2
		h ^= k
	}
	h ^= uint32(n)
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

var reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func extractTitle(body []byte) string {
	m := reTitle.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(m[1])))
}

func netLookupHost(ctx context.Context, host string) ([]string, error) {
	var r net.Resolver
	ips, err := r.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if validIP(ip) {
			out = append(out, ip)
		}
	}
	sort.Strings(out)
	return out, nil
}
