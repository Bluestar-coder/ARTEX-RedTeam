#!/usr/bin/env bash
# Reproducible M2 CT backend benchmark (crt.sh / crt.name / certspotter).
#
#   ./scripts/recon-ct-bench/run.sh [seeds] [runs]
#   defaults: seeds=example.com,python.org runs=5
#
# Needs outbound HTTPS to the three services. Emits CTBENCH CSV lines (also
# printed as test logs) so the table in README.md can be recomputed.
set -euo pipefail
REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
SEEDS=${1:-example.com,python.org}
RUNS=${2:-5}
cd "$REPO_ROOT"
CT_BENCH=1 CT_BENCH_SEEDS="$SEEDS" CT_BENCH_RUNS="$RUNS" \
  go test ./internal/recon -run TestCTBackendBench -count=1 -v -timeout 30m | tee /tmp/recon-ct-bench.log
echo
echo "CSV lines are prefixed with CTBENCH in /tmp/recon-ct-bench.log"
