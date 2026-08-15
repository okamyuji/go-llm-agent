#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/11-japanese-ux.md 5.2 節 (D-16)
# 27 日本語 TTY の E2E スクリプト。
# fixtures/tty_exercise が creack/pty で擬似端末を割り当てて agent バイナリを起動し、
# CJK 入力の編集・カーソル移動・Backspace・Ctrl-R 検索・幅描画を検証する。
# 擬似端末を割り当てられない環境では fixture が tty_skipped=true を出して skip する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building tty exerciser${NC}\n"
go build -o "$WORK/tty" ./tests/e2e/fixtures/tty_exercise

printf "${YELLOW}>>> running tty exerciser${NC}\n"
set +e
"$WORK/tty" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: tty exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit "$RUN_EXIT"
fi

if grep -q '^tty_skipped=true' "$WORK/out.log"; then
  printf "${YELLOW}SKIP: 擬似端末を割り当てられないため skip${NC}\n"
  exit 0
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

check_true "tty_bracketed_paste_on"
check_true "tty_prompt_shown"
check_true "tty_cursor_left_cjk"
check_true "tty_backspace_erases_cells"
check_true "tty_wrap_pads_cjk"
check_true "tty_search_prompt"
check_true "tty_search_candidate"
check_true "tty_search_older_candidate"
check_true "tty_search_abort_restores"
check_true "tty_search_submits_candidate"
check_true "tty_bracketed_paste_off"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: PTY 経由の CJK 編集・カーソル移動・Ctrl-R 検索・幅描画が期待どおり動作した${NC}\n"
