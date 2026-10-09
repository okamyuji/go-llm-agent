#!/usr/bin/env bash
# 30 REPL の /image が画像を data URI の content parts として llamacpp provider へ送ることを確かめる E2E スクリプト。
# fixtures/image_input_exercise が OpenAI 互換 SSE スタブを fixture 内に立てる。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building image input exerciser${NC}\n"
go build -o "$WORK/image" ./tests/e2e/fixtures/image_input_exercise

printf "${YELLOW}>>> running image input exerciser${NC}\n"
set +e
"$WORK/image" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: image input exerciser exited with %d${NC}\n" "$RUN_EXIT"
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

check_true "image_parts_sent"
check_true "missing_image_reported"
check_true "answer_shown"
if ! grep -q '^image_turns=1$' "$WORK/out.log"; then
  printf "${RED}FAIL: image_turns want=1 got=%s${NC}\n" "$(grep -o '^image_turns=.*' "$WORK/out.log" || echo '<missing>')"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: /image が画像を content parts として送り、読めない画像は送らずに報告した${NC}\n"
