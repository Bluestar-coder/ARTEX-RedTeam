package jsintel

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/tdewolff/parse/v2"
	"github.com/tdewolff/parse/v2/js"
)

// FileInput is one JS unit for workspace analysis. A workspace is a set of
// files that share a host (bundle boundary); constants are propagated across
// files in the same workspace, which is the main advantage over regex rules.
type FileInput struct {
	Host      string
	PageURL   string
	ScriptURL string
	Code      string
}

// Analysis is the AST result for a workspace.
type Analysis struct {
	Interfaces []Interface
	Notes      []Obfuscation
	ParseFails []string
}

type dkind int

const (
	dLiteral dkind = iota
	dTemplate
	dConcat
	dTernary
	dIdent
	dMember
	dIndex
	dObject
	dArray
	dUnknown
)

type desc struct {
	kind  dkind
	lit   string
	parts []desc          // template parts / concat operands / ternary then-else
	name  string          // ident name / member property
	obj   map[string]desc // object literal
	arr   []desc          // array literal
	note  string          // why an expression is not statically evaluable
}

type constEntry struct {
	value desc
	file  int
}

type astFile struct {
	in     FileInput
	text   string
	cursor int
}

// AnalyzeFiles parses each file twice (first for constant declarations, then
// for call sites) to avoid retaining multi-MB ASTs. It never executes JS.
func AnalyzeFiles(files []FileInput) Analysis {
	a := &Analysis{}
	consts := map[string]constEntry{}
	bases := map[string]desc{}
	parsed := make([]*astFile, 0, len(files))
	for i, in := range files {
		ast, err := parseJS(in.Code)
		if err != nil {
			a.ParseFails = append(a.ParseFails, in.ScriptURL+": "+err.Error())
			continue
		}
		af := &astFile{in: in, text: in.Code}
		parsed = append(parsed, af)
		collectDecls(ast, af, i, consts, bases)
	}
	for _, af := range parsed {
		ast, err := parseJS(af.in.Code)
		if err != nil {
			continue
		}
		walkCalls(ast, func(call *js.CallExpr) {
			ifaces, notes := analyzeCall(af, call, consts, bases)
			a.Interfaces = append(a.Interfaces, ifaces...)
			a.Notes = append(a.Notes, notes...)
		})
	}
	a.Interfaces = dedupeASTInterfaces(a.Interfaces)
	return *a
}

func parseJS(code string) (*js.AST, error) {
	return js.Parse(parse.NewInput(bytes.NewReader([]byte(code))), js.Options{})
}

func walkCalls(ast *js.AST, fn func(*js.CallExpr)) {
	js.Walk(callVisitor{fn: fn}, ast)
}

type callVisitor struct{ fn func(*js.CallExpr) }

func (v callVisitor) Enter(n js.INode) js.IVisitor {
	if c, ok := n.(*js.CallExpr); ok {
		v.fn(c)
	}
	return v
}
func (v callVisitor) Exit(js.INode) {}

func collectDecls(ast *js.AST, af *astFile, fileIdx int, consts map[string]constEntry, bases map[string]desc) {
	js.Walk(visitorFunc(func(n js.INode) {
		vd, ok := n.(*js.VarDecl)
		if !ok {
			return
		}
		for _, be := range vd.List {
			v, ok := be.Binding.(*js.Var)
			if !ok || be.Default == nil {
				continue
			}
			name := nameOfVar(v)
			if name == "" {
				continue
			}
			// axios.create({baseURL: "/api"}) instance
			if call, ok := be.Default.(*js.CallExpr); ok && calleeText(call.X) == "axios.create" && len(call.Args.List) > 0 {
				if obj, ok := call.Args.List[0].Value.(*js.ObjectExpr); ok {
					if p := objectProp(obj, "baseURL"); p != nil {
						bases[name] = toDesc(p)
						continue
					}
				}
			}
			consts[name] = constEntry{value: toDesc(be.Default), file: fileIdx}
		}
	}), ast)
}

func objectProp(obj *js.ObjectExpr, key string) js.IExpr {
	for _, p := range obj.List {
		if p.Name == nil {
			continue
		}
		name := strings.Trim(string(p.Name.Literal.Data), `"'`)
		if name == key {
			return p.Value
		}
	}
	return nil
}

type visitorFunc func(js.INode)

func (f visitorFunc) Enter(n js.INode) js.IVisitor { f(n); return f }
func (f visitorFunc) Exit(js.INode)                {}

func nameOfVar(v *js.Var) string { return string(v.Data) }

func calleeText(e js.IExpr) string {
	switch n := e.(type) {
	case *js.Var:
		return nameOfVar(n)
	case *js.DotExpr:
		base := calleeText(n.X)
		prop := propText(n.Y)
		if base != "" && prop != "" {
			return base + "." + prop
		}
	}
	return ""
}

func propText(e js.IExpr) string {
	switch n := e.(type) {
	case js.LiteralExpr:
		return strings.Trim(string(n.Data), `"'`)
	case *js.LiteralExpr:
		return strings.Trim(string(n.Data), `"'`)
	case *js.Var:
		return nameOfVar(n)
	}
	return ""
}

func literalDesc(n js.LiteralExpr) desc {
	switch n.TokenType {
	case js.StringToken, js.TemplateToken:
		return desc{kind: dLiteral, lit: unquoteJS(string(n.Data))}
	case js.NumericToken:
		return desc{kind: dLiteral, lit: string(n.Data)}
	}
	return desc{kind: dUnknown, note: "literal:" + string(n.Data)}
}

func toDesc(e js.IExpr) desc {
	switch n := e.(type) {
	case js.LiteralExpr:
		return literalDesc(n)
	case *js.LiteralExpr:
		return literalDesc(*n)
	case *js.Var:
		return desc{kind: dIdent, name: nameOfVar(n)}
	case *js.TemplateExpr:
		var parts []desc
		for i, p := range n.List {
			val := string(p.Value)
			if i == 0 {
				val = strings.TrimPrefix(val, "`")
			} else {
				val = strings.TrimPrefix(val, "}")
			}
			val = strings.TrimSuffix(val, "${")
			parts = append(parts, desc{kind: dLiteral, lit: val})
			parts = append(parts, toDesc(p.Expr))
		}
		tail := strings.TrimPrefix(string(n.Tail), "}")
		tail = strings.TrimSuffix(tail, "`")
		parts = append(parts, desc{kind: dLiteral, lit: tail})
		return desc{kind: dTemplate, parts: parts}
	case *js.BinaryExpr:
		if n.Op == js.AddToken {
			return desc{kind: dConcat, parts: []desc{toDesc(n.X), toDesc(n.Y)}}
		}
		return desc{kind: dUnknown, note: "binary_expr"}
	case *js.CondExpr:
		return desc{kind: dTernary, parts: []desc{toDesc(n.X), toDesc(n.Y)}}
	case *js.DotExpr:
		return desc{kind: dMember, name: propText(n.Y), parts: []desc{toDesc(n.X)}}
	case *js.IndexExpr:
		return desc{kind: dIndex, parts: []desc{toDesc(n.X), toDesc(n.Y)}}
	case *js.ObjectExpr:
		obj := map[string]desc{}
		for _, p := range n.List {
			if p.Name == nil {
				continue
			}
			key := strings.Trim(string(p.Name.Literal.Data), `"'`)
			if key == "" {
				continue
			}
			obj[key] = toDesc(p.Value)
		}
		return desc{kind: dObject, obj: obj}
	case *js.ArrayExpr:
		arr := make([]desc, 0, len(n.List))
		for _, el := range n.List {
			if el.Value == nil {
				arr = append(arr, desc{kind: dUnknown, note: "array_hole"})
				continue
			}
			arr = append(arr, toDesc(el.Value))
		}
		return desc{kind: dArray, arr: arr}
	case *js.GroupExpr:
		return toDesc(n.X)
	case *js.CallExpr:
		return desc{kind: dUnknown, note: "call:" + calleeText(n.X)}
	case *js.NewExpr:
		return desc{kind: dUnknown, note: "new_expr"}
	}
	return desc{kind: dUnknown, note: "unsupported_expr"}
}

func unquoteJS(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		q := s[0]
		if (q == '\'' || q == '"' || q == '`') && s[len(s)-1] == q {
			s = s[1 : len(s)-1]
		}
	}
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\'`, `'`)
	s = strings.ReplaceAll(s, "\\`", "`")
	return s
}

// evalDesc evaluates an expression with at most maxHops constant lookups.
func evalDesc(d desc, consts map[string]constEntry, bases map[string]desc, hops int) (value string, params []string, folded bool, unresolved []string) {
	switch d.kind {
	case dLiteral:
		return d.lit, nil, false, nil
	case dTemplate, dConcat:
		var b strings.Builder
		for _, p := range d.parts {
			v, ps, f, un := evalDesc(p, consts, bases, hops)
			params = append(params, ps...)
			unresolved = append(unresolved, un...)
			if v == "" && len(un) > 0 {
				name := exprPlaceholder(p)
				b.WriteString("{" + name + "}")
				params = append(params, name)
				continue
			}
			b.WriteString(v)
			if f {
				folded = true
			}
		}
		return b.String(), params, folded, unresolved
	case dTernary:
		x, px, fx, ux := evalDesc(d.parts[0], consts, bases, hops)
		if x != "" {
			return x, px, fx, ux
		}
		return evalDesc(d.parts[1], consts, bases, hops)
	case dIdent:
		if hops >= 5 {
			return "", nil, false, []string{"exceeded_hops:" + d.name}
		}
		if c, ok := consts[d.name]; ok {
			v, p, _, un := evalDesc(c.value, consts, bases, hops+1)
			return v, p, true, un
		}
		if b, ok := bases[d.name]; ok {
			v, p, _, un := evalDesc(b, consts, bases, hops+1)
			return v, p, true, un
		}
		if isRuntimeName(d.name) {
			return "", nil, false, []string{"runtime_constant:" + d.name}
		}
		return "", []string{d.name}, false, []string{"unresolved_constant:" + d.name}
	case dMember:
		base := d.parts[0]
		if base.kind == dIdent && isRuntimeName(base.name) {
			return "", nil, false, []string{"runtime_constant:" + base.name + "." + d.name}
		}
		bv, bp, bf, _ := evalDesc(base, consts, bases, hops)
		if base.kind == dObject {
			if v, ok := base.obj[d.name]; ok {
				x, p, _, un := evalDesc(v, consts, bases, hops)
				return x, append(bp, p...), true, un
			}
		}
		_ = bv
		return "", bp, bf, []string{"dynamic_member:" + d.name}
	case dIndex:
		bv, bp, _, _ := evalDesc(d.parts[0], consts, bases, hops)
		if d.parts[0].kind == dArray {
			iv, _, _, _ := evalDesc(d.parts[1], consts, bases, hops)
			var idx int
			if n, err := strconv.Atoi(strings.TrimSpace(iv)); err == nil && n >= 0 && n < len(d.parts[0].arr) {
				idx = n
				x, p, _, un := evalDesc(d.parts[0].arr[idx], consts, bases, hops)
				return x, append(bp, p...), true, un
			}
		}
		_ = bv
		return "", bp, false, []string{"dynamic_index"}
	case dObject:
		return "", nil, false, []string{"object_expr"}
	case dArray:
		return "", nil, false, []string{"array_expr"}
	}
	return "", nil, false, []string{d.note}
}

func isRuntimeName(name string) bool {
	switch name {
	case "process", "window", "document", "globalThis", "location", "navigator", "this", "import", "require":
		return true
	}
	return false
}

func exprPlaceholder(d desc) string {
	switch d.kind {
	case dIdent:
		return d.name
	case dMember:
		return d.name
	}
	return "expr"
}

func (f *astFile) locate(callee string) int {
	needle := callee + "("
	if idx := strings.Index(f.text[f.cursor:], needle); idx >= 0 {
		pos := f.cursor + idx
		f.cursor = pos + len(needle)
		return pos
	}
	if idx := strings.Index(f.text, needle); idx >= 0 {
		f.cursor = idx + len(needle)
		return idx
	}
	if idx := strings.Index(f.text, callee); idx >= 0 {
		return idx
	}
	return 0
}

func analyzeCall(af *astFile, call *js.CallExpr, consts map[string]constEntry, bases map[string]desc) ([]Interface, []Obfuscation) {
	callee := calleeText(call.X)
	if callee == "" || callee == "axios.create" {
		return nil, nil
	}
	args := call.Args.List
	var out []Interface
	var notes []Obfuscation
	start := af.locate(callee)

	add := func(urlExpr js.IExpr, method, rule string, literalExpected bool) {
		if urlExpr == nil {
			return
		}
		d := toDesc(urlExpr)
		value, params, folded, unresolved := evalDesc(d, consts, bases, 0)
		if value == "" || strings.HasPrefix(value, "{") || strings.Contains(value, "://") && hostOfValue(value) == "" {
			detail := "empty_or_unresolved"
			if len(unresolved) > 0 {
				detail = strings.Join(unresolved, ";")
			}
			if strings.Contains(af.text[start:minInt(len(af.text), start+240)], "baseUrl") {
				detail = "runtime_base_url: " + detail
			}
			notes = append(notes, Obfuscation{Host: af.in.Host, Reason: "needs_manual", Detail: detail,
				ScriptURL: af.in.ScriptURL, Source: sourceAt(af.in.PageURL, af.in.ScriptURL, af.text, start)})
			return
		}
		conf := ConfidenceMedium
		if d.kind == dLiteral && method != "unknown" {
			conf = ConfidenceHigh
		} else if method == "unknown" && d.kind == dLiteral && !folded {
			conf = ConfidenceLow
		}
		c := buildIface(af.in.Host, af.in.PageURL, af.in.ScriptURL, af.text, value, start, rule, "")
		c.method = method
		c.confidence = conf
		_ = literalExpected
		it := &Interface{Host: c.host, Method: c.method, Path: c.path, Params: uniq(append(params, c.params...)), Tags: uniq(c.tags), Rule: c.rule, Confidence: c.confidence, Sources: []SourceRef{c.ref}}
		out = append(out, *it)
	}

	methodFromOpts := func(obj *js.ObjectExpr) string {
		if p := objectProp(obj, "method"); p != nil {
			if v, _, _, _ := evalDesc(toDesc(p), consts, bases, 0); v != "" {
				return strings.ToUpper(v)
			}
		}
		return "unknown"
	}

	switch {
	case callee == "fetch" || callee == "window.fetch":
		var opts *js.ObjectExpr
		if len(args) > 1 {
			opts, _ = args[1].Value.(*js.ObjectExpr)
		}
		method := "unknown"
		if opts != nil {
			method = methodFromOpts(opts)
		}
		add(args[0].Value, method, "ast_fetch", false)
	case strings.HasPrefix(callee, "axios.") && len(args) > 0:
		verb := strings.ToUpper(strings.TrimPrefix(callee, "axios."))
		if validVerb(verb) {
			add(args[0].Value, verb, "ast_axios", false)
		}
	case strings.HasSuffix(callee, ".open") && len(args) >= 2:
		method := "unknown"
		if v, _, _, _ := evalDesc(toDesc(args[0].Value), consts, bases, 0); v != "" {
			method = strings.ToUpper(v)
		}
		add(args[1].Value, method, "ast_xhr", false)
	case strings.HasPrefix(callee, "navigator.sendBeacon"):
		add(args[0].Value, "POST", "ast_sendbeacon", false)
	case callee == "$.get" || callee == "jQuery.get" || callee == "$.getJSON" || callee == "jQuery.getJSON":
		add(args[0].Value, "GET", "ast_jquery", false)
	case callee == "$.post" || callee == "jQuery.post":
		add(args[0].Value, "POST", "ast_jquery", false)
	case callee == "$.ajax" || callee == "jQuery.ajax":
		if len(args) > 0 {
			if obj, ok := args[0].Value.(*js.ObjectExpr); ok {
				if u := objectProp(obj, "url"); u != nil {
					add(u, methodFromOpts(obj), "ast_jquery_ajax", false)
				}
			}
		}
	default:
		// axios instance: const api = axios.create({baseURL:"/api"}); api.get("/user")
		if i := strings.Index(callee, "."); i > 0 {
			baseName, verb := callee[:i], strings.ToUpper(callee[i+1:])
			if base, ok := bases[baseName]; ok && validVerb(verb) && len(args) > 0 {
				d := toDesc(args[0].Value)
				value, params, folded, unresolved := evalDesc(d, consts, bases, 0)
				baseVal, _, _, _ := evalDesc(base, consts, bases, 0)
				if value != "" && baseVal != "" {
					combined := joinBaseURL(baseVal, value)
					c := buildIface(af.in.Host, af.in.PageURL, af.in.ScriptURL, af.text, combined, start, "ast_axios_baseurl", "")
					c.method = verb
					c.confidence = ConfidenceMedium
					if d.kind == dLiteral && !folded {
						c.confidence = ConfidenceHigh
					}
					out = append(out, Interface{Host: c.host, Method: c.method, Path: c.path, Params: uniq(append(params, c.params...)), Tags: uniq(c.tags), Rule: c.rule, Confidence: c.confidence, Sources: []SourceRef{c.ref}})
				} else if len(unresolved) > 0 || value == "" {
					notes = append(notes, Obfuscation{Host: af.in.Host, Reason: "needs_manual", Detail: strings.Join(unresolved, ";"),
						ScriptURL: af.in.ScriptURL, Source: sourceAt(af.in.PageURL, af.in.ScriptURL, af.text, start)})
				}
				return out, notes
			}
		}
		// {url: ..., method: ...} passed directly to a request-like helper
		if len(args) > 0 {
			if obj, ok := args[0].Value.(*js.ObjectExpr); ok {
				if u := objectProp(obj, "url"); u != nil {
					add(u, methodFromOpts(obj), "ast_config_object", false)
				}
			}
		}
	}
	return out, notes
}

func validVerb(v string) bool {
	switch v {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
		return true
	}
	return false
}

func dedupeASTInterfaces(in []Interface) []Interface {
	seen := map[string]*Interface{}
	order := []string{}
	for i := range in {
		it := in[i]
		key := it.Host + "|" + it.Method + "|" + strings.ToLower(it.Path)
		if cur, ok := seen[key]; ok {
			cur.Params = uniq(append(cur.Params, it.Params...))
			cur.Tags = uniq(append(cur.Tags, it.Tags...))
			for _, s := range it.Sources {
				cur.Sources = appendSource(cur.Sources, s)
			}
			if confidenceRank(it.Confidence) > confidenceRank(cur.Confidence) {
				cur.Confidence = it.Confidence
				cur.Rule = it.Rule
			}
			continue
		}
		cp := it
		seen[key] = &cp
		order = append(order, key)
	}
	out := make([]Interface, 0, len(order))
	for _, k := range order {
		out = append(out, *seen[k])
	}
	return out
}
