#!/usr/bin/env python3
"""Apply the route/handler split described by plan.json (mechanical move only)."""
import json
import os
import sys

ROOT = sys.argv[1]
PLAN = json.load(open(sys.argv[2]))

DOMAIN_CAP = {
    "auth": "Auth", "system": "System", "tasks": "Tasks", "side": "Side",
    "assets": "Assets", "findings": "Findings", "traffic": "Traffic",
    "platform": "Platform", "report": "Report",
}
ORDER = ["auth", "system", "tasks", "side", "assets", "findings", "traffic", "platform", "report"]


def read(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read().split("\n")


def write(path, lines):
    with open(path, "w", encoding="utf-8") as fh:
        fh.write("\n".join(lines))


def block(lines, start, end):  # 1-based inclusive
    return lines[start - 1:end]


server_dir = os.path.join(ROOT, "server")
files = {name: read(os.path.join(server_dir, name))
         for name in ("server.go", "server_mgmt.go", "side_questions.go", "finding_traffic.go")}

# 1) collect registration text per domain (keep leading comments + trailing comments)
regs = {d: [] for d in DOMAIN_CAP}
for r in PLAN["registrations"]:
    assert r["file"] == "server.go"
    start = r["lead_start"] if r["lead_start"] else r["start_line"]
    regs[r["domain"]].extend(block(files["server.go"], start, r["end_line"]))
    regs[r["domain"]].append("")
# 2) collect method text per domain
methods = {d: [] for d in DOMAIN_CAP}
for m in PLAN["methods"]:
    methods[m["domain"]].extend(block(files[m["file"]], m["start_line"], m["end_line"]))
    methods[m["domain"]].append("")
# 3) collect moved whole functions
funcs = {}
for f in PLAN["funcs"]:
    funcs[f["name"]] = "\n".join(block(files[f["file"]], f["start_line"], f["end_line"]))

# 4) build deletion line sets per file
delete = {name: set() for name in files}
for r in PLAN["registrations"]:
    start = r["lead_start"] if r["lead_start"] else r["start_line"]
    delete["server.go"].update(range(start, r["end_line"] + 1))
for m in PLAN["methods"]:
    delete[m["file"]].update(range(m["start_line"], m["end_line"] + 1))
for f in PLAN["funcs"]:
    delete[f["file"]].update(range(f["start_line"], f["end_line"] + 1))

# the explicit finding-traffic registration call is folded into registerFindingsRoutes
finding_call = None
for i, line in enumerate(files["server.go"], 1):
    if "s.registerFindingTraffic(mux)" in line:
        finding_call = i
        break
assert finding_call, "registerFindingTraffic call not found"
delete["server.go"].add(finding_call)

# 5) handler call block (insert right after mux creation / side registration)
calls = ["\t// Domain route registrations. Paths, methods and middleware are unchanged;",
         "\t// this file only assembles them, each domain lives in routes_<domain>.go."]
for d in ORDER:
    if d == "side":
        calls.append("\ts.registerConversationRoutes(mux)")
        calls.append("\ts.registerSideRoutes(mux)")
    elif regs[d]:
        calls.append(f"\ts.register{DOMAIN_CAP[d]}Routes(mux)")
calls.append("")

anchor = None
for i, line in enumerate(files["server.go"], 1):
    if "s.registerSideRoutes(mux)" in line:
        anchor = i
        break
assert anchor, "registerSideRoutes call not found"

# 6) rewrite server.go
out = []
for i, line in enumerate(files["server.go"], 1):
    if i in delete["server.go"]:
        continue
    out.append(line)
    if i == anchor:
        out.extend(calls)
files["server.go"] = out

for name in ("server_mgmt.go", "side_questions.go", "finding_traffic.go"):
    files[name] = [line for i, line in enumerate(files[name], 1) if i not in delete[name]]
    files[name] = [line for line in files[name]]


def emit_domain(domain):
    cap = DOMAIN_CAP[domain]
    parts = ["package server", "", 'import (', '\t"net/http"', ")", ""]
    if regs[domain]:
        fn = f"register{cap}Routes"
        if domain == "side":
            fn = "registerConversationRoutes"
        parts.append(f"// {fn} registers the {domain} HTTP routes. Paths, methods and")
        parts.append("// handlers are unchanged from the pre-split table.")
        parts.append(f"func (s *Server) {fn}(mux *http.ServeMux) {{")
        parts.extend(regs[domain])
        if domain == "findings":
            parts.append("\ts.registerFindingTraffic(mux)")
        parts.append("}")
        parts.append("")
    if domain == "side":
        parts.append(funcs["registerSideRoutes"])
        parts.append("")
    if domain == "findings":
        parts.append(funcs["registerFindingTraffic"])
        parts.append("")
    parts.extend(methods[domain])
    return parts


for domain in ORDER:
    if not regs[domain] and not methods[domain] and domain not in ("side", "findings"):
        continue
    write(os.path.join(server_dir, f"routes_{domain}.go"), emit_domain(domain))

for name, lines in files.items():
    write(os.path.join(server_dir, name), lines)

print("split applied")
