#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 08 HITL ツール承認の E2E スクリプト。
# fixtures/approval_exercise が Request/Submit と timeout default-deny を確認する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building approval exerciser${NC}\n"
go build -o "$WORK/ap" ./tests/e2e/fixtures/approval_exercise

printf "${YELLOW}>>> running approval exerciser${NC}\n"
set +e
"$WORK/ap" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: approval exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit 1
fi
if ! grep -q 'approval_allowed=true' "$WORK/out.log"; then
  printf "${RED}FAIL: expected approval_allowed=true${NC}\n"
  exit 1
fi
if ! grep -q 'timeout_allowed=false' "$WORK/out.log"; then
  printf "${RED}FAIL: expected timeout_allowed=false (default deny)${NC}\n"
  exit 1
fi

printf "${GREEN}OK: HTTP Approver request/submit and timeout-deny work as expected${NC}\n"
