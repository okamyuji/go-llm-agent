#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/04-approval.md 5.2 節
# 22 対話承認の E2E スクリプト。
# fixtures/approval_prompt_exercise がパイプ駆動の in-process 構成で
# y / n / timeout / 致命的失敗 / 既読レジストリの各シナリオを確認する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building approval prompt exerciser${NC}\n"
go build -o "$WORK/ape" ./tests/e2e/fixtures/approval_prompt_exercise

printf "${YELLOW}>>> running approval prompt exerciser${NC}\n"
set +e
"$WORK/ape" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: approval prompt exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit "$RUN_EXIT"
fi

fail=0
check_true() {
  local key="$1"
  local got
  got="$(grep -o "^${key}=.*" "$WORK/out.log" || true)"
  if [[ "$got" != "${key}=true" ]]; then
    printf "${RED}FAIL: %s want=true got=%s${NC}\n" "$key" "${got:-<missing>}"
    fail=1
  fi
}

check_true "approval_yes_writes_file"
check_true "approval_shows_diff"
check_true "approval_no_skips_write"
check_true "approval_timeout_denies"
check_true "approval_fatal_error_aborts_turn"
check_true "approval_summary_keeps_registry_clean"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: 対話承認の y/n・diff preview・timeout・致命的失敗が期待どおり動作した${NC}\n"
