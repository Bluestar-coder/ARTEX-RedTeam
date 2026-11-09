package recon

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/chromedp/cdproto/network"
)

// customHeaderNames returns names only; header values are never stored.
func customHeaderNames(h network.Headers) []string {
	standard := map[string]bool{
		"accept": true, "accept-encoding": true, "accept-language": true,
		"cache-control": true, "connection": true, "content-length": true,
		"content-type": true, "cookie": true, "dnt": true, "host": true,
		"origin": true, "pragma": true, "priority": true, "referer": true,
		"sec-ch-ua": true, "sec-ch-ua-mobile": true, "sec-ch-ua-platform": true,
		"sec-fetch-dest": true, "sec-fetch-mode": true, "sec-fetch-site": true,
		"sec-fetch-user": true, "upgrade-insecure-requests": true, "user-agent": true,
	}
	var out []string
	for k := range h {
		lk := strings.ToLower(k)
		if standard[lk] || strings.HasPrefix(lk, "sec-") || strings.HasPrefix(lk, "if-") || strings.HasPrefix(lk, ":") {
			continue
		}
		out = append(out, lk)
	}
	sort.Strings(out)
	return out
}

// paramNames extracts only names from JSON/urlencoded bodies. Values never
// leave this function.
func paramNames(postData string) []string {
	if strings.TrimSpace(postData) == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(postData), &v) == nil {
		var names []string
		collectJSONKeys(v, "", &names)
		sort.Strings(names)
		return names
	}
	if vals, err := url.ParseQuery(postData); err == nil {
		var names []string
		for k := range vals {
			names = append(names, k)
		}
		sort.Strings(names)
		return names
	}
	return nil
}

func collectJSONKeys(v any, prefix string, names *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			name := k
			if prefix != "" {
				name = prefix + "." + k
			}
			*names = append(*names, name)
			collectJSONKeys(child, name, names)
		}
	case []any:
		for _, child := range x {
			collectJSONKeys(child, prefix, names)
		}
	}
}
