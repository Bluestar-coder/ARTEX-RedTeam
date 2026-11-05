// Package toolbuild is the single construction entry point for every LLM-facing
// tool defined by this repository.
//
// Before this package existed, agent and server each had their own read/write
// constructor pair (agent.readTool/writeTool and server.roTool/wrTool) and built
// their parameter schemas with package-local helpers. Adding a recon tool meant
// copying whichever pair lived nearby, so the two copies slowly drifted. Build is
// now the one place that turns a Spec into an actool.CoreTool:
//
//   - it validates required fields at construction time (name, description,
//     schema, handler, access and permission), so a tool that forgets to declare
//     read-only or its permission policy fails immediately instead of running
//     with fail-closed defaults nobody noticed;
//   - it keeps the exact semantics of the old constructors: read-only tools are
//     marked concurrency-safe with the allow policy, mutating tools are neither
//     read-only nor concurrency-safe, and the allow policy is permission.Allowed();
//   - it accepts an optional per-call Timeout that wraps the handler in a
//     context deadline. Current tools pass 0 (no deadline), which preserves the
//     old behavior exactly; the harness still owns the run-level wall-clock
//     budget, so no per-tool budget is invented here.
//
// Build never changes a tool's name, description, schema or return value: it is
// a constructor, not a schema transformer.
package toolbuild

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/norma/permission"
	actool "github.com/Bluestar-coder/ARTEX-RedTeam/norma/tool"
)

// Access declares whether a tool may mutate state. It is required: Build panics
// on the zero value so a new tool cannot "forget" to say.
type Access int

const (
	AccessUnset Access = iota
	AccessReadOnly
	AccessReadWrite
)

// Permission is the registration-time permission policy. It is required: Build
// panics on the zero value. Almost every built-in tool allows without asking;
// MCP tools ask the user, exactly as before.
type Permission int

const (
	PermissionUnset Permission = iota
	PermissionAllow
	PermissionAsk
)

// RunFunc is the handler shape accepted by Build. It receives the harness tool
// context (some host tools, e.g. MCP/custom commands, need it for output capture
// and proxy env).
type RunFunc func(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error)

// Spec describes one tool.
type Spec struct {
	Name        string
	Description string
	Schema      map[string]any
	Run         RunFunc

	Access     Access
	Permission Permission
	// Concurrent marks the tool safe to run alongside other tools. The old
	// read-only constructors set it; write tools and host tools did not.
	Concurrent bool
	// Prompt is an optional extra prompt fragment (actool.Spec.Prompt).
	Prompt string
	// RawInput marks a tool that parses its own arguments (actool.Spec.RawInput).
	RawInput bool
	// AskPrompt overrides the confirmation text for PermissionAsk. Empty means
	// actool's default "approve <name>?".
	AskPrompt string
	// Timeout, when >0, wraps Run in a context deadline. 0 = inherit the
	// caller's context (the behavior of every tool before this package existed).
	Timeout time.Duration
}

// Build validates s and returns the tool. It panics on an invalid spec: tools
// are registered at process startup, so a panic is the loudest possible failure
// and happens before any model can see a half-defined tool.
func Build(s Spec) actool.CoreTool {
	if s.Name == "" {
		panic("toolbuild: tool has empty Name")
	}
	if s.Schema == nil {
		panic(fmt.Sprintf("toolbuild: tool %q has nil Schema", s.Name))
	}
	if s.Run == nil {
		panic(fmt.Sprintf("toolbuild: tool %q has nil Run", s.Name))
	}
	if s.Access != AccessReadOnly && s.Access != AccessReadWrite {
		panic(fmt.Sprintf("toolbuild: tool %q must declare Access (read-only or read-write)", s.Name))
	}
	if s.Permission != PermissionAllow && s.Permission != PermissionAsk {
		panic(fmt.Sprintf("toolbuild: tool %q must declare Permission (allow or ask)", s.Name))
	}
	if s.Timeout < 0 {
		panic(fmt.Sprintf("toolbuild: tool %q has negative Timeout", s.Name))
	}

	run := s.Run
	if s.Timeout > 0 {
		inner := run
		run = func(ctx context.Context, input json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			ctx, cancel := context.WithTimeout(ctx, s.Timeout)
			defer cancel()
			return inner(ctx, input, tc)
		}
	}

	spec := actool.Spec{
		Name:        s.Name,
		Description: s.Description,
		Prompt:      s.Prompt,
		Schema:      s.Schema,
		RawInput:    s.RawInput,
		Run:         run,
		Permissions: permissionFunc(s),
	}
	if s.Access == AccessReadOnly {
		spec.ReadOnly = func(json.RawMessage) bool { return true }
	}
	if s.Concurrent {
		spec.Concurrent = func(json.RawMessage) bool { return true }
	}
	return actool.Build(spec)
}

func permissionFunc(s Spec) func(context.Context, json.RawMessage, permission.Context) permission.Decision {
	switch s.Permission {
	case PermissionAsk:
		prompt := s.AskPrompt
		if prompt == "" {
			prompt = "approve " + s.Name + "?"
		}
		return func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.AskUser(prompt)
		}
	default:
		return func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		}
	}
}

// Read is the convenience form for the common read-only, concurrency-safe,
// allow-without-asking tool. It exists so the vast majority of call sites stay
// one line; it is a thin declaration over Build, not a second implementation.
func Read(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return Build(Spec{
		Name:        name,
		Description: desc,
		Schema:      schema,
		Access:      AccessReadOnly,
		Permission:  PermissionAllow,
		Concurrent:  true,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// Write is the convenience form for the common mutating, allow-without-asking
// tool (not read-only, not concurrency-safe) — the old writeTool/wrTool behavior.
func Write(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return Build(Spec{
		Name:        name,
		Description: desc,
		Schema:      schema,
		Access:      AccessReadWrite,
		Permission:  PermissionAllow,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// --- schema assembly helpers -------------------------------------------------
//
// These are the one place that turns the small JSON-Schema shapes into maps. The
// old packages had obj/str/intp/idp (agent) and objSchema/strParam (server); both
// produced the same JSON, so delegating them here removes the drift without
// changing what the model sees.

// Object builds {"type":"object","properties":<props>} plus optional required.
func Object(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// String builds {"type":"string"} plus a description when non-empty.
func String(desc string) map[string]any {
	m := map[string]any{"type": "string"}
	if desc != "" {
		m["description"] = desc
	}
	return m
}

// Integer builds {"type":"integer"} plus a description when non-empty.
func Integer(desc string) map[string]any {
	m := map[string]any{"type": "integer"}
	if desc != "" {
		m["description"] = desc
	}
	return m
}

// Boolean builds {"type":"boolean"} plus a description when non-empty.
func Boolean(desc string) map[string]any {
	m := map[string]any{"type": "boolean"}
	if desc != "" {
		m["description"] = desc
	}
	return m
}

// Array builds {"type":"array","items":<items>} plus a description when non-empty.
func Array(items map[string]any, desc string) map[string]any {
	m := map[string]any{"type": "array", "items": items}
	if desc != "" {
		m["description"] = desc
	}
	return m
}

// EnumString builds {"type":"string","enum":<values>} plus a description when
// non-empty. An empty value list panics: an enum with no choices is a bug.
func EnumString(desc string, values ...string) map[string]any {
	if len(values) == 0 {
		panic("toolbuild: EnumString with no values")
	}
	m := map[string]any{"type": "string", "enum": values}
	if desc != "" {
		m["description"] = desc
	}
	return m
}
