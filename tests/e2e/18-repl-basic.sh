#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/05-streaming.md 5.2 節 /
# 10-slash-commands.md 5.2 節
# 18 REPL 基本ターンとスラッシュコマンドの E2E スクリプト。
# fixtures/repl_basic_exercise が OpenAI 互換 SSE スタブ (D-17) を fixture 内に立て、
# パイプ駆動の REPL で日本語・絵文字の往復と /help /model /cost を確認する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building repl basic exerciser${NC}\n"
go build -o "$WORK/repl" ./tests/e2e/fixtures/repl_basic_exercise

printf "${YELLOW}>>> running repl basic exerciser${NC}\n"
set +e
"$WORK/repl" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: repl basic exerciser exited with %d${NC}\n" "$RUN_EXIT"
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

check_true "repl_delta_utf8_ok"
check_true "repl_help_ok"
check_true "repl_model_ok"
check_true "repl_cost_ok"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: REPL の 1 ターン応答とスラッシュコマンドが期待どおり動作した${NC}\n"
