#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 05 tool_choice と JSON スキーマ検証の E2E スクリプト。
# OpenAI provider 互換のフェイクサーバに required モードを送信し、
# ペイロードの tool_choice が "required" になっていることを観測する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building tool_choice exerciser${NC}\n"
go build -o "$WORK/exercise" ./tests/e2e/fixtures/tool_choice_exercise

printf "${YELLOW}>>> running exerciser${NC}\n"
set +e
"$WORK/exercise" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit "$RUN_EXIT"
fi
if ! grep -q 'tool_choice_payload=required' "$WORK/out.log"; then
  printf "${RED}FAIL: tool_choice payload was not required${NC}\n"
  exit 1
fi

printf "${GREEN}OK: tool_choice required mode mapped correctly to OpenAI payload${NC}\n"
