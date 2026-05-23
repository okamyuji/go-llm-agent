#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 13 プロンプトテンプレート版管理の E2E スクリプト。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

printf "${YELLOW}>>> running TestLoader_* and TestRenderer_*${NC}\n"
go test -race -run 'TestLoader|TestRenderer' ./internal/prompt/... 2>&1 | tail -5
RUN_EXIT=${PIPESTATUS[0]}
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: prompt template tests failed${NC}\n"
  exit 1
fi
printf "${GREEN}OK: prompt template loader and renderer behave as expected${NC}\n"
