#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/03-fs-edit.md 5.2 節
# 21 部分編集 (fs_edit) の E2E スクリプト。
# fixtures/fs_edit_exercise が fs_read 必須・old_string 完全一致置換・
# 一意一致要求 (replace_all=false) を確認する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building fs_edit exerciser${NC}\n"
go build -o "$WORK/fe" ./tests/e2e/fixtures/fs_edit_exercise

printf "${YELLOW}>>> running fs_edit exerciser${NC}\n"
set +e
"$WORK/fe" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: fs_edit exerciser exited with %d${NC}\n" "$RUN_EXIT"
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

check_true "edit_before_read_denied"
check_true "edit_after_read_ok"
check_true "only_target_line_changed"
check_true "ambiguous_match_denied"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: fs_edit requires prior fs_read, replaces exact matches, and rejects ambiguous matches${NC}\n"
