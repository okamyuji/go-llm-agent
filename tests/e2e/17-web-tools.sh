#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 17 web_search / web_fetch の E2E スクリプト。httptest とスタブ webgrab で実ネットワーク非依存に検証する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building web exerciser${NC}\n"
go build -o "$WORK/web" ./tests/e2e/fixtures/web_exercise

printf "${YELLOW}>>> running web exerciser${NC}\n"
set +e
"$WORK/web" > "$WORK/out.log" 2>&1
RC=$?
set -e
cat "$WORK/out.log"
if [[ "$RC" -ne 0 ]]; then
  printf "${RED}FAIL: web exerciser exited with %d${NC}\n" "$RC"
  exit "$RC"
fi
if ! grep -q 'search_ok=true' "$WORK/out.log"; then
  printf "${RED}FAIL: web_search did not extract results${NC}\n"
  exit 1
fi
if ! grep -q 'fetch_ok=true' "$WORK/out.log"; then
  printf "${RED}FAIL: web_fetch did not return markdown with paging guidance${NC}\n"
  exit 1
fi
if ! grep -q 'agent_web_flow_ok=true' "$WORK/out.log"; then
  printf "${RED}FAIL: agent did not execute web_search then web_fetch before its first LLM request${NC}\n"
  exit 1
fi

printf "${GREEN}OK: web_search and web_fetch tools work without real network${NC}\n"
