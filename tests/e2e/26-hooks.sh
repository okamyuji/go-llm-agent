#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/09-hooks.md 5.2 節
# 26 pre/post ツール実行フックの E2E スクリプト。
# fixtures/hooks_exercise が touch_probe フェイクツールに対して deny / allow /
# post payload / fail-open / 親キャンセルの各シナリオを確認する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building hooks exerciser${NC}\n"
go build -o "$WORK/hooks" ./tests/e2e/fixtures/hooks_exercise

printf "${YELLOW}>>> running hooks exerciser${NC}\n"
set +e
"$WORK/hooks" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: hooks exerciser exited with %d${NC}\n" "$RUN_EXIT"
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

check_true "hook_pre_deny_blocks"
check_true "hook_pre_allow_passes"
check_true "hook_post_receives_result"
check_true "hook_pre_timeout_allows"
check_true "hook_parent_cancel_blocks"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: pre hook の exit 2 でツールがブロックされ exit 0 で通った${NC}\n"
