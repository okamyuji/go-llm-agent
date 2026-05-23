#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 03 LLM リトライとバックオフの E2E スクリプト。
# tests/e2e/fixtures/retry_exercise を実行し、429 を 2 回返してから成功する
# fake provider に対してリトライが期待通り走ることを確認する。
# ローカル PC 固有の API キーや課金には依存しない。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building retry exerciser${NC}\n"
go build -o "$WORK/retry-exercise" ./tests/e2e/fixtures/retry_exercise

printf "${YELLOW}>>> running retry exerciser${NC}\n"
"$WORK/retry-exercise" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: retry exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit 1
fi

if ! grep -qE 'calls=3' "$WORK/out.log"; then
  printf "${RED}FAIL: expected calls=3 in output${NC}\n"
  exit 1
fi
if ! grep -qE 'content="done"' "$WORK/out.log"; then
  printf "${RED}FAIL: expected content=\"done\" in output${NC}\n"
  exit 1
fi

printf "${GREEN}OK: retry decorator retried 2 failures and reached success${NC}\n"
