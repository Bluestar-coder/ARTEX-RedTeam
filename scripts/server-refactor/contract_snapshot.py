#!/usr/bin/env python3
"""Black-box HTTP contract snapshot for the server refactor.

For every route in scripts/server-refactor/routes-after.tsv it sends a fixed
probe matrix to two servers (before-ref and after-ref, each on a fresh database),
records status / content-type / body, normalises volatile values (JWT, timestamps,
temp paths) and diffs the two snapshots.

"Request schema" is captured as the probe definition per route (the same concrete
request sent to both sides); response schema + error-message set are the recorded
status/content-type/body. Handlers do not declare JSON response schemas, so this
black-box matrix is the only machine-comparable contract evidence.

Prereqs: Docker PG container (default artex-routes-pg) reachable; Go toolchain.
"""
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request

REPO = os.path.abspath(sys.argv[1])
BEFORE_REF = sys.argv[2]
AFTER_REF = sys.argv[3]
OUT = os.path.abspath(sys.argv[4]) if len(sys.argv) > 4 else "/tmp/server-contract"
PG_CONTAINER = os.environ.get("PG_CONTAINER", "artex-routes-pg")
ROUTES_TSV = os.path.join(REPO, "scripts/server-refactor/routes-after.tsv")

SKIP_FRAGMENTS = (
    "/stream", "/events", "/api/update/apply", "/api/update/rollback",
    "/api/update/check", "/api/llm/test", "/api/mcp", "/api/logs",
    "/download", "/export", "/upload", "/api/workspace/upload",
    "/api/selfupdate", "/api/gc",
)

NORM = [
    (re.compile(r"eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}"), "<jwt>"),
    (re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})"), "<ts>"),
    (re.compile(r"\b1[6-9]\d{8}(?:\d{3})?\b"), "<unix>"),
    (re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"), "<uuid>"),
    (re.compile(r"/tmp/[^\"\s]*snap-[^\"\s]*"), "<datadir>"),
]


def norm(body):
    for rx, rep in NORM:
        body = rx.sub(rep, body)
    return body


def run(cmd, **kw):
    return subprocess.run(cmd, check=True, capture_output=True, text=True, **kw)


def pg(sql, db="postgres"):
    run(["docker", "exec", PG_CONTAINER, "psql", "-U", "artex", "-d", db, "-c", sql])


def req(port, method, path, body=None, token=None, timeout=8):
    data = None if body is None else body.encode()
    r = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=data, method=method)
    r.add_header("content-type", "application/json")
    if token:
        r.add_header("authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            raw = resp.read(40000).decode("utf-8", "replace")
            return {"status": resp.status, "ctype": resp.headers.get("content-type", ""), "body": norm(raw)}
    except urllib.error.HTTPError as e:
        raw = e.read(40000).decode("utf-8", "replace")
        return {"status": e.code, "ctype": e.headers.get("content-type", ""), "body": norm(raw)}
    except Exception as e:  # timeout / connection reset
        return {"status": "ERR", "ctype": "", "body": norm(f"{type(e).__name__}: {e}")}


def routes():
    out = []
    with open(ROUTES_TSV, encoding="utf-8") as fh:
        for line in fh:
            parts = line.rstrip("\n").split("\t")
            if len(parts) != 4:
                continue
            method, pattern, _handler, _auth = parts
            path = pattern
            if path.startswith(method + " "):
                path = path[len(method) + 1:]
            path = re.sub(r"\{[^}]+\}", "1", path)
            skip = False
            for frag in SKIP_FRAGMENTS:
                if frag in path:
                    skip = True
                    break
            if skip:
                continue
            out.append((method, path))
    out.sort(key=lambda x: (x[1], x[0]))
    return out


def probes(method):
    # (probe id, body, authenticated)
    ps = [("auth-empty", None if method in ("GET", "DELETE") else "{}", True),
          ("noauth-empty", None if method in ("GET", "DELETE") else "{}", False)]
    if method in ("POST", "PUT", "PATCH"):
        ps.append(("auth-malformed", "{", True))
    return ps


def start_server(side, ref):
    wt = os.path.join(OUT, f"wt-{side}")
    bin_path = os.path.join(OUT, f"artex-{side}")
    data = os.path.join(OUT, f"data-snap-{side}")
    port = 18080 if side == "before" else 18081
    db = f"snap_{side}"
    run(["git", "-C", REPO, "worktree", "add", "--detach", wt, ref])
    run(["go", "build", "-o", bin_path, "./cmd/artex-redteam"], cwd=wt)
    pg(f"DROP DATABASE IF EXISTS {db}")
    pg(f"CREATE DATABASE {db}")
    env = {k: v for k, v in os.environ.items() if not k.startswith(("OPENAI_", "ANTHROPIC_", "ARTEX_REDTEAM_LLM"))}
    env["ARTEX_REDTEAM_PG_DSN"] = f"postgres://artex:artextest@127.0.0.1:55442/{db}?sslmode=disable"
    proc = subprocess.Popen(
        [bin_path, "-data", data, "-addr", f"127.0.0.1:{port}", "-proxy", "", "-proxy-pool-gateway", ""],
        env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return wt, proc, port


def stop_server(wt, proc):
    proc.terminate()
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
    run(["git", "-C", REPO, "worktree", "remove", "--force", wt])


def snapshot(side, ref):
    wt, proc, port = start_server(side, ref)
    try:
        for _ in range(60):
            if req(port, "GET", "/api/health")["status"] == 200:
                break
            time.sleep(0.5)
        init = req(port, "POST", "/api/auth/init", json.dumps({"password": "snap-Admin-Pass!"}))
        token = ""
        try:
            token = json.loads(init["body"]).get("token", "")
        except Exception:
            login = req(port, "POST", "/api/auth/login",
                        json.dumps({"username": "ARTEX-RedTeam", "password": "snap-Admin-Pass!"}))
            try:
                token = json.loads(login["body"]).get("token", "")
            except Exception:
                pass
        records = []
        for method, path in routes():
            for pid, body, authed in probes(method):
                rec = req(port, method, path, body, token if authed else None)
                records.append({"method": method, "path": path, "probe": pid,
                                "status": rec["status"], "ctype": rec["ctype"], "body": rec["body"]})
        json.dump({"side": side, "ref": ref, "records": records}, open(os.path.join(OUT, f"contract-{side}.json"), "w"),
                  ensure_ascii=False, sort_keys=True, indent=2)
        print(f"{side}: {len(records)} probes")
    finally:
        stop_server(wt, proc)


def main():
    shutil.rmtree(OUT, ignore_errors=True)
    os.makedirs(OUT)
    snapshot("before", BEFORE_REF)
    snapshot("after", AFTER_REF)
    a = json.load(open(os.path.join(OUT, "contract-before.json")))["records"]
    b = json.load(open(os.path.join(OUT, "contract-after.json")))["records"]
    diff = []
    if len(a) != len(b):
        diff.append({"length": [len(a), len(b)]})
    for x, y in zip(a, b):
        if x != y:
            diff.append({"before": x, "after": y})
    json.dump(diff, open(os.path.join(OUT, "contract.diff.json"), "w"), ensure_ascii=False, indent=2)
    print(f"probes={len(a)} differing={len(diff)}")
    print("IDENTICAL" if not diff else "DIFF (see contract.diff.json)")


if __name__ == "__main__":
    main()
