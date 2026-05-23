#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 12 MCP クライアント discovery の E2E スクリプト。
# fixtures/mcp_echo_server をビルドして stdio JSON-RPC で tools/list と
# tools/call を実行し、レスポンスを観測する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

printf "${YELLOW}>>> running TestClient_ListAndCall and TestClient_UnknownMethodReturnsError${NC}\n"
set +e
go test -race -timeout 120s -run 'TestClient_(ListAndCall|UnknownMethodReturnsError|.*Empty.*)' ./internal/mcp/... 2>&1 | tail -10
RUN_EXIT=${PIPESTATUS[0]}
set -e
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: MCP discovery tests failed${NC}\n"
  exit 1
fi
printf "${GREEN}OK: MCP stdio client can list and call tools${NC}\n"
