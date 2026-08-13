#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/02-truncation.md 5.4 節
# 20 ツール結果切詰めの E2E スクリプト。
# fixtures/truncation_exercise が head 60% + tail 40% 切詰めと
# EventToolResult の全文保持、max_chars=-1 の無効化を確認する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building truncation exerciser${NC}\n"
go build -o "$WORK/tr" ./tests/e2e/fixtures/truncation_exercise

printf "${YELLOW}>>> running truncation exerciser${NC}\n"
set +e
"$WORK/tr" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: truncation exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit "$RUN_EXIT"
fi

fail=0
check_eq() {
  local key="$1" want="$2"
  local got
  got="$(grep -o "^${key}=.*" "$WORK/out.log" || true)"
  if [[ "$got" != "${key}=${want}" ]]; then
    printf "${RED}FAIL: %s want=%s got=%s${NC}\n" "$key" "$want" "${got:-<missing>}"
    fail=1
  fi
}

check_eq "history_content_chars" "8033"
check_eq "contains_marker" "true"
check_eq "contains_tail_exit_code" "true"
check_eq "event_tool_result_full_chars" "9000"
check_eq "disabled_history_content_chars" "9000"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: tool result truncation (head 60%%/tail 40%%, marker, full EventToolResult, disable via -1) works as expected${NC}\n"
