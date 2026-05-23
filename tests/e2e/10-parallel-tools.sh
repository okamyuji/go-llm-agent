#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 10 並列ツール実行の E2E スクリプト。既存ユニットテスト TestExecuteToolsParallel_*
# を -race 付きで再現可能であることを確認する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

printf "${YELLOW}>>> running ExecuteToolsParallel tests with -race${NC}\n"
go test -race -run TestExecuteToolsParallel ./internal/agent/... 2>&1 | tail -10
RUN_EXIT=$?
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: parallel tool tests failed${NC}\n"
  exit 1
fi
printf "${GREEN}OK: parallel tool execution is race-free and order-preserving${NC}\n"
