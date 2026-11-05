#!/usr/bin/env bash
# Reproducible before/after contract diff for the tool-constructor refactor.
#
#   ./scripts/tool-contract/run.sh [before-ref] [after-ref] [outdir]
#   defaults: before=f53a6fd^  after=f53a6fd  outdir=/tmp/tool-contract
#
# It checks out both refs as detached worktrees, drops a temporary dumper into
# each (then deletes it), serialises every LLM-facing tool (project + SDK
# families) and diffs the two canonical JSON files.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
BEFORE_REF=${1:-f53a6fd^}
AFTER_REF=${2:-f53a6fd}
OUT=${3:-/tmp/tool-contract}

rm -rf "$OUT"
mkdir -p "$OUT"

cleanup() {
  for wt in "$OUT/wt-before" "$OUT/wt-after"; do
    git -C "$REPO_ROOT" worktree remove --force "$wt" >/dev/null 2>&1 || true
    rm -rf "$wt"
  done
}
trap cleanup EXIT

snapshot() {
  local ref=$1
  local side=$2
  local wt="$OUT/wt-$side"
  git -C "$REPO_ROOT" worktree add --detach "$wt" "$ref" >/dev/null
  mkdir -p "$wt/internal/toolsnap"
  cp "$REPO_ROOT/scripts/tool-contract/dump_agent.go.txt" "$wt/internal/toolsnap/main.go"
  cp "$REPO_ROOT/scripts/tool-contract/dump_server_test.go.txt" "$wt/server/zz_toolsnap_test.go"

  (cd "$wt" && go run ./internal/toolsnap) >"$OUT/agent-$side.jsonl"
  (cd "$wt" && TOOLSNAP_OUT="$OUT/server-$side.jsonl" go test ./server -run TestZZToolSnapshot >/dev/null)

  rm -rf "$wt/internal/toolsnap" "$wt/server/zz_toolsnap_test.go"
  python3 "$REPO_ROOT/scripts/tool-contract/combine.py" \
    "$OUT/agent-$side.jsonl" "$OUT/server-$side.jsonl" >"$OUT/tools-$side.json"
  git -C "$REPO_ROOT" worktree remove --force "$wt" >/dev/null
}

echo "[1/4] snapshot before: $BEFORE_REF"
snapshot "$BEFORE_REF" before
echo "[2/4] snapshot after:  $AFTER_REF"
snapshot "$AFTER_REF" after

echo "[3/4] diff"
if diff -u "$OUT/tools-before.json" "$OUT/tools-after.json" >"$OUT/tools.diff"; then
  echo "IDENTICAL: tools-before.json == tools-after.json"
else
  echo "DIFF FOUND (see $OUT/tools.diff)"
fi

echo "[4/4] hashes / counts"
if command -v shasum >/dev/null 2>&1; then HASH=shasum; else HASH=sha256sum; fi
$HASH -a 256 "$OUT/tools-before.json" "$OUT/tools-after.json" 2>/dev/null ||
  sha256sum "$OUT/tools-before.json" "$OUT/tools-after.json"
python3 - "$OUT/tools-before.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
print("total=%d by_family=%s" % (d["total"], d["by_family"]))
PY
