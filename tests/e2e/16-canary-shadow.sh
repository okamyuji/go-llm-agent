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
# go test 全量出力を残しつつ末尾だけ stdout に簡約表示する
LOG=$(mktemp)
trap 'rm -f "$LOG"' EXIT
set +e
go test -race -run TestRouter ./internal/agent/... > "$LOG" 2>&1
RUN_EXIT=$?
set -e
tail -n 200 "$LOG"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: Router tests failed (see %s)${NC}\n" "$LOG"
  exit "$RUN_EXIT"
fi
printf "${GREEN}OK: Router.Pick is deterministic and shadow ratio is capped at 0.5${NC}\n"
