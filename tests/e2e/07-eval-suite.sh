#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 07 評価フレームワーク CLI の E2E スクリプト。
# fixtures/eval_exercise が LoadSuite + Score + WriteReport の一連動作を検証する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building eval exerciser${NC}\n"
go build -o "$WORK/eval" ./tests/e2e/fixtures/eval_exercise

printf "${YELLOW}>>> running eval exerciser${NC}\n"
set +e
"$WORK/eval" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: eval exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit "$RUN_EXIT"
fi
if ! grep -q 'aggregate_cases=1' "$WORK/out.log"; then
  printf "${RED}FAIL: expected aggregate_cases=1${NC}\n"
  exit 1
fi
if ! grep -q 'aggregate_passed=1' "$WORK/out.log"; then
  printf "${RED}FAIL: expected aggregate_passed=1${NC}\n"
  exit 1
fi

printf "${GREEN}OK: eval Scorer and Reporter produce passing aggregate${NC}\n"
