package agent

import _ "embed"

// The built-in editable prompt bodies live as Markdown files so prompt review and
// policy changes are reviewable without reading Go string literals. They are
// embedded at build time and seeded into agent_prompts; the DB remains the
// authoritative editable copy for deployments.
//
//go:embed prompts/planner.md
var plannerDefaultTmpl string

//go:embed prompts/worker.md
var workerDefaultTmpl string
