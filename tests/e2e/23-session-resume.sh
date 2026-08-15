#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/06-session-resume.md 5.2 節
# 23 セッション記録・-resume 復元の E2E スクリプト。
# fixtures/session_resume_exercise が cmdChat と同じ公開関数 (cliui.ChatSessionsDir /
# cliui.ResumeLatestSession) を呼び、実 LLM に依存しないスクリプト provider で検証する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building session resume exerciser${NC}\n"
go build -o "$WORK/sre" ./tests/e2e/fixtures/session_resume_exercise

printf "${YELLOW}>>> running session resume exerciser${NC}\n"
set +e
"$WORK/sre" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: session resume exerciser exited with %d${NC}\n" "$RUN_EXIT"
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

check_true "session1_file_created"
check_true "chat_dir_fallback_ok"
check_true "resume_flag_path_ok"
check_true "session2_sees_session1"
check_true "resume_empty_dir_ok"
check_true "broken_line_skipped"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: chat セッションの記録と -resume による復元が機能した${NC}\n"
