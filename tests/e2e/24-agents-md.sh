#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/07-agents-md.md 5.4 節
# 24 AGENTS.md 自動読み込みの E2E スクリプト。
# fixtures/agents_md_exercise が agent.LoadAgentsMD + composeSystemPrompt 相当の合成を
# 通し、実 LLM に依存しないスクリプト provider で system メッセージへの反映を検証する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building agents_md exerciser${NC}\n"
go build -o "$WORK/ame" ./tests/e2e/fixtures/agents_md_exercise

printf "${YELLOW}>>> running agents_md exerciser${NC}\n"
set +e
"$WORK/ame" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: agents_md exerciser exited with %d${NC}\n" "$RUN_EXIT"
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

check_true "agents_md_prompt_applied"
check_true "agents_md_absent_ok"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: AGENTS.md の自動読み込みと合成がシステムプロンプトへ反映された${NC}\n"
