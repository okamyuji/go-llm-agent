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
set +e
go test -race -timeout 60s -run 'TestPIIRedactor|TestChainRedactor' ./internal/safety/... 2>&1 | tail -5
RUN_EXIT=${PIPESTATUS[0]}
set -e
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: PII redactor tests failed${NC}\n"
  exit 1
fi

# HTTP API の non-stream で chunk 境界を跨ぐ PII が漏れずに redact されることを確認する
# (実機検証で検出した /v1/chat/completions のリーク回帰を防ぐ)
printf "${YELLOW}>>> running TestChat_NonStreaming_PIIChunkCrossingRedacted${NC}\n"
set +e
go test -race -timeout 60s -run 'TestChat_NonStreaming_PIIChunkCrossingRedacted|TestChat_NonStreaming_NoRedactor_NoOp' ./internal/transport/httpapi/... 2>&1 | tail -5
RUN_EXIT=${PIPESTATUS[0]}
set -e
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: HTTP API PII chunk-crossing test failed${NC}\n"
  exit 1
fi
printf "${GREEN}OK: PII redactor masks email/phone/ipv4 and survives chunk boundaries in HTTP non-stream${NC}\n"
