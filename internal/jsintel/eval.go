package jsintel

import (
	"regexp"
	"strings"
)

var (
	reConstAssign = regexp.MustCompile(`(?m)(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(["'` + "`" + `])((?:[^\\]|\\.){1,300}?)(["'` + "`" + `])`)
	reAxiosCreate = regexp.MustCompile(`(?s)(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*axios\s*\.\s*create\s*\(\s*\{[^}]{0,500}?baseURL\s*:\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`)
	reTemplateVar = regexp.MustCompile(`\$\{([^}]+)\}`)
)

// fileConstMap collects single-level literal assignments in this file only:
//
//	const BASE = "/api";  let V1 = "/v1"
//
// No cross-file tracing and no re-assignment/data-flow analysis (M2 boundary).
func fileConstMap(text string) map[string]string {
	out := map[string]string{}
	for _, m := range reConstAssign.FindAllStringSubmatch(text, -1) {
		name, val := m[1], m[3]
		if strings.Contains(val, "${") {
			continue // template assignment is handled at use site
		}
		if strings.HasPrefix(val, "/") || strings.Contains(val, "://") {
			out[name] = val
		}
	}
	return out
}

// axiosBaseURLs maps axios instance variables to their one-level baseURL.
func axiosBaseURLs(text string) map[string]string {
	out := map[string]string{}
	for _, m := range reAxiosCreate.FindAllStringSubmatch(text, -1) {
		out[m[1]] = strings.TrimRight(m[2], "/")
	}
	return out
}

// readCallArg returns the first top-level argument starting at the "(" position
// (the paren itself). It respects quotes/backticks and nested (), [], {}.
func readCallArg(text string, open int) (expr string, end int, ok bool) {
	if open < 0 || open >= len(text) || text[open] != '(' {
		return "", -1, false
	}
	depth := 0
	var quote byte
	for i := open + 1; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				if c == ')' {
					return strings.TrimSpace(text[open+1 : i]), i, true
				}
				return "", -1, false
			}
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(text[open+1 : i]), i, true
			}
		}
	}
	return "", -1, false
}

// readValueExpr reads a config-object value (stop at top-level , or }).
func readValueExpr(text string, start int) (string, int, bool) {
	depth := 0
	var quote byte
	for i := start; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				return strings.TrimSpace(text[start:i]), i, true
			}
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(text[start:i]), i, true
			}
		}
	}
	return "", -1, false
}

// splitTopPlus splits a concatenation expression on top-level '+'.
func splitTopPlus(expr string) []string {
	var out []string
	var quote byte
	depth := 0
	last := 0
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case '+':
			if depth == 0 {
				out = append(out, strings.TrimSpace(expr[last:i]))
				last = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(expr[last:]))
	return out
}

// evalPathExpr evaluates a one-level path expression. It returns:
//   - path: literal/folded path (may include {variable} placeholders)
//   - params: variables used as placeholders
//   - confidence: medium when any folding/placeholder happened, high when pure literal
//   - unresolved: number of identifiers that could not be folded
//   - used: false when the expression does not produce a path at all
func evalPathExpr(expr string, consts, baseURLs map[string]string) (path string, params []string, confidence Confidence, unresolved int, used bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", nil, ConfidenceLow, 0, false
	}
	confidence = ConfidenceHigh
	var b strings.Builder
	first := true
	firstUnresolved := false
	for _, term := range splitTopPlus(expr) {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if s, ok := parseJSString(term); ok {
			decoded, vars := expandTemplateVars(s.value)
			// Template vars that are not simple identifiers (this.baseUrl, foo.bar)
			// cannot be folded one level; count them as unresolved.
			badVars := 0
			for _, m := range reTemplateVar.FindAllStringSubmatch(s.value, -1) {
				if !isSimpleIdent(strings.TrimSpace(m[1])) {
					badVars++
				}
			}
			unresolved += badVars
			b.WriteString(decoded)
			params = append(params, vars...)
			if s.template || len(vars) > 0 {
				confidence = ConfidenceMedium
			}
			first = false
			continue
		}
		// identifier / member expression
		name := strings.TrimSpace(term)
		if v, ok := consts[name]; ok {
			b.WriteString(v)
			confidence = ConfidenceMedium
			first = false
			continue
		}
		if v, ok := baseURLs[name]; ok {
			b.WriteString(v)
			confidence = ConfidenceMedium
			first = false
			continue
		}
		if isSimpleIdent(name) {
			unresolved++
			params = append(params, name)
			if first && b.Len() == 0 {
				firstUnresolved = true
			}
			b.WriteString("{" + name + "}")
			confidence = ConfidenceMedium
			first = false
			continue
		}
		// Unknown expression (this.baseUrl, function call, member access): cannot
		// fold without data-flow; count as unresolved.
		unresolved++
		if first && b.Len() == 0 {
			firstUnresolved = true
		}
		first = false
	}
	path = strings.TrimSpace(b.String())
	if path == "" {
		return "", params, confidence, unresolved, false
	}
	// Only keep values that can plausibly be paths.
	if !strings.HasPrefix(path, "/") && !strings.Contains(path, "://") {
		return "", params, confidence, unresolved, false
	}
	if firstUnresolved || strings.HasPrefix(path, "{") {
		return "", params, confidence, unresolved, false
	}
	return path, params, confidence, unresolved, true
}

type jsString struct {
	value    string
	template bool
}

func parseJSString(term string) (jsString, bool) {
	if len(term) < 2 {
		return jsString{}, false
	}
	q := term[0]
	if (q != '\'' && q != '"' && q != '`') || term[len(term)-1] != q {
		return jsString{}, false
	}
	value := term[1 : len(term)-1]
	if q == '`' {
		value = strings.ReplaceAll(value, "\\`", "`")
		return jsString{value: value, template: true}, true
	}
	value = strings.ReplaceAll(value, `\"`, `"`)
	value = strings.ReplaceAll(value, `\'`, `'`)
	return jsString{value: value}, true
}

// expandTemplateVars replaces ${name} with {name} and returns the variable names.
// ${constName} is substituted by evalPathExpr? We keep it as a placeholder here;
// simple literals in templates are handled by lookup before reaching this point.
func expandTemplateVars(s string) (string, []string) {
	var params []string
	out := reTemplateVar.ReplaceAllStringFunc(s, func(m string) string {
		inner := strings.TrimSpace(m[2 : len(m)-1])
		if isSimpleIdent(inner) {
			params = append(params, inner)
			return "{" + inner + "}"
		}
		return "{" + inner + "}"
	})
	return out, params
}

func isSimpleIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func confidenceRank(c Confidence) int {
	switch c {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	default:
		return 1
	}
}

func maxConfidence(a, b Confidence) Confidence {
	if confidenceRank(b) > confidenceRank(a) {
		return b
	}
	return a
}

func hostOfValue(raw string) string {
	v := strings.TrimSpace(raw)
	if i := strings.Index(v, "://"); i >= 0 {
		rest := v[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[:j]
		}
		if j := strings.IndexByte(rest, ':'); j >= 0 {
			rest = rest[:j]
		}
		return strings.ToLower(rest)
	}
	return strings.ToLower(v)
}
