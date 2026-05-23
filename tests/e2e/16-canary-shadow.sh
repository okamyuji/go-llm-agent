#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 16 カナリア・シャドウデプロイの E2E スクリプト。
# Router.Pick の境界値テストと shadow ratio の 0.5 上限を -race 付きで実行する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

printf "${YELLOW}>>> running TestRouter_*${NC}\n"
go test -race -run TestRouter ./internal/agent/... 2>&1 | tail -5
RUN_EXIT=$?
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: Router tests failed${NC}\n"
  exit 1
fi
printf "${GREEN}OK: Router.Pick is deterministic and shadow ratio is capped at 0.5${NC}\n"
