#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 14 PII リダクションの E2E スクリプト。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

printf "${YELLOW}>>> running TestPIIRedactor_* and TestChainRedactor_*${NC}\n"
go test -race -run 'TestPIIRedactor|TestChainRedactor' ./internal/safety/... 2>&1 | tail -5
RUN_EXIT=${PIPESTATUS[0]}
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: PII redactor tests failed${NC}\n"
  exit 1
fi
printf "${GREEN}OK: PII redactor masks email/phone/ipv4 and chains with OutputRedactor${NC}\n"
