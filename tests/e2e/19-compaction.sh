#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/01-compaction.md 5.4 節
# 19 会話履歴圧縮の E2E スクリプト。
# fixtures/compaction_exercise が REPL の自動発火 (shouldCompact) と /compact 手動発火、
# no-op 報告を stub LLM (D-17) 相手に確認する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building compaction exerciser${NC}\n"
go build -o "$WORK/cmp" ./tests/e2e/fixtures/compaction_exercise

printf "${YELLOW}>>> running compaction exerciser${NC}\n"
set +e
"$WORK/cmp" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: compaction exerciser exited with %d${NC}\n" "$RUN_EXIT"
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

check_true "compaction_auto"
check_true "compaction_manual"
check_true "compaction_no_consecutive_user"
check_true "compaction_noop_reported"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: 自動発火と /compact 手動発火で履歴が要約に置換され応答が継続した${NC}\n"
