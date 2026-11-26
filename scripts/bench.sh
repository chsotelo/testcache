#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 2 ]; then
  echo "Usage: $0 <project-dir> <vitest target> [extra vitest args...]" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT_DIR="$(cd "$1" && pwd)"
shift

TMP_BUILD_DIR="$(mktemp -d "$REPO_ROOT/.bench-bin.XXXXXX")"
cleanup() {
  rm -rf "$TMP_BUILD_DIR"
}
trap cleanup EXIT

TC_BIN="${TC_BIN:-$TMP_BUILD_DIR/tc}"
if [ ! -x "$TC_BIN" ]; then
  (cd "$REPO_ROOT" && go build -o "$TC_BIN" ./cmd/tc)
fi

time_cmd_ms() {
  python3 -c '
import subprocess, sys, time
cwd = sys.argv[1]
cmd = sys.argv[2:]
t0 = time.perf_counter()
proc = subprocess.run(cmd, cwd=cwd)
dt_ms = (time.perf_counter() - t0) * 1000.0
print(f"__BENCH_MS__={dt_ms:.1f}", file=sys.stderr)
sys.exit(proc.returncode)
' "$@"
}

echo "Project: $PROJECT_DIR"
echo "Target:  $*"
echo ""

echo "--- 1. Baseline: pnpm exec vitest run $* ---"
BASE_ERR="$TMP_BUILD_DIR/base.err"
time_cmd_ms "$PROJECT_DIR" pnpm exec vitest run "$@" 2> >(tee "$BASE_ERR" >&2)
BASE_MS="$(grep '^__BENCH_MS__=' "$BASE_ERR" | tail -n 1 | cut -d= -f2)"

echo ""
echo "--- 2. tc clean + tc vitest $* (first run, cold cache) ---"
(cd "$PROJECT_DIR" && "$TC_BIN" clean)
COLD_ERR="$TMP_BUILD_DIR/cold.err"
time_cmd_ms "$PROJECT_DIR" "$TC_BIN" vitest "$@" 2> >(tee "$COLD_ERR" >&2)
COLD_MS="$(grep '^__BENCH_MS__=' "$COLD_ERR" | tail -n 1 | cut -d= -f2)"

echo ""
echo "--- 3. tc vitest $* (second run, warm cache) ---"
WARM_ERR="$TMP_BUILD_DIR/warm.err"
time_cmd_ms "$PROJECT_DIR" "$TC_BIN" vitest "$@" 2> >(tee "$WARM_ERR" >&2)
WARM_MS="$(grep '^__BENCH_MS__=' "$WARM_ERR" | tail -n 1 | cut -d= -f2)"

echo ""
echo "Benchmark results ($PROJECT_DIR :: $*):"
printf "  baseline (pnpm exec vitest run): %8s ms\n" "$BASE_MS"
printf "  tc vitest (1st run, cold):       %8s ms\n" "$COLD_MS"
printf "  tc vitest (2nd run, cached):     %8s ms\n" "$WARM_MS"
