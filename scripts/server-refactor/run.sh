#!/usr/bin/env bash
# Reproducible route-table diff for the server route/handler split.
#
#   ./scripts/server-refactor/run.sh <before-ref> [after-ref] [outdir]
#   e.g. ./scripts/server-refactor/run.sh 7a92f9b HEAD
#
# Checks both refs out as detached worktrees, runs the AST route extractor
# (scripts/server-refactor/routeinv2.go.txt) and diffs the normalised tables
# (method / pattern / handler / auth).
set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
BEFORE_REF=${1:?usage: run.sh <before-ref> [after-ref] [outdir]}
AFTER_REF=${2:-HEAD}
OUT=${3:-/tmp/server-route-diff}

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
  mkdir -p "$wt/internal/srvref"
  cp "$REPO_ROOT/scripts/server-refactor/routeinv2.go.txt" "$wt/internal/srvref/main.go"
  (cd "$wt" && go run ./internal/srvref) >"$OUT/routes-$side.tsv"
  rm -rf "$wt/internal/srvref"
  git -C "$REPO_ROOT" worktree remove --force "$wt" >/dev/null
  awk -F'\t' 'NR>1{print $1"\t"$2"\t"$3"\t"$5}' "$OUT/routes-$side.tsv" >"$OUT/routes-$side.norm"
  echo "$side routes: $(( $(wc -l < "$OUT/routes-$side.tsv") - 1 ))"
}

snapshot "$BEFORE_REF" before
snapshot "$AFTER_REF" after

if diff -u "$OUT/routes-before.norm" "$OUT/routes-after.norm" >"$OUT/routes.diff"; then
  echo "IDENTICAL: route table diff is empty"
else
  echo "DIFF FOUND: $OUT/routes.diff"
fi
