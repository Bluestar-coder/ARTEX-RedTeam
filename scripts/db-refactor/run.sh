#!/usr/bin/env bash
# Reproducible SQL snapshot diff for the db query/scan refactor.
#
#   ./scripts/db-refactor/run.sh <before-ref> [after-ref] [outdir]
#   e.g. ./scripts/db-refactor/run.sh 0d2bc7f HEAD
#
# Snapshots every SQL string literal (whitespace-normalised multiset) from Go
# sources in both refs and diffs them. SQL text must not change in this refactor,
# so the expected result is an empty diff.
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
BEFORE_REF=${1:?usage: run.sh <before-ref> [after-ref] [outdir]}
AFTER_REF=${2:-HEAD}
OUT=${3:-/tmp/db-sql-diff}

rm -rf "$OUT"
mkdir -p "$OUT"

cleanup() {
  git -C "$REPO_ROOT" worktree remove --force "$OUT/wt-before" >/dev/null 2>&1 || true
  rm -rf "$OUT/wt-before"
}
trap cleanup EXIT

git -C "$REPO_ROOT" worktree add --detach "$OUT/wt-before" "$BEFORE_REF" >/dev/null
python3 "$REPO_ROOT/scripts/db-refactor/sql_snapshot.py" "$OUT/wt-before" >"$OUT/sql-before.json"
python3 "$REPO_ROOT/scripts/db-refactor/sql_snapshot.py" "$REPO_ROOT" >"$OUT/sql-after.json"

if diff -u "$OUT/sql-before.json" "$OUT/sql-after.json" >"$OUT/sql.diff"; then
  echo "IDENTICAL: SQL snapshot diff is empty"
else
  echo "DIFF FOUND: $OUT/sql.diff"
fi
python3 - "$OUT/sql-before.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
print("distinct=%d total=%d" % (d["distinct"], d["total"]))
PY
