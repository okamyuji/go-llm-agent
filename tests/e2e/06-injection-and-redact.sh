#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 06 入出力フィルタと出力リダクションの E2E スクリプト。
# fixtures/safety_exercise を実行して Scanner と Redactor が期待通り動くことを検証する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building safety exerciser${NC}\n"
go build -o "$WORK/safety" ./tests/e2e/fixtures/safety_exercise

printf "${YELLOW}>>> running safety exerciser${NC}\n"
"$WORK/safety" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: safety exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit 1
fi
if ! grep -q 'pattern=ignore_previous' "$WORK/out.log"; then
  printf "${RED}FAIL: expected scanner to flag ignore_previous${NC}\n"
  exit 1
fi
if ! grep -q '\[REDACTED:OPENAI\]' "$WORK/out.log"; then
  printf "${RED}FAIL: expected redactor to mask OPENAI key${NC}\n"
  exit 1
fi

printf "${GREEN}OK: prompt injection scanner and output redactor enforced as expected${NC}\n"
