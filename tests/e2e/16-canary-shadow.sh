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
# 失敗時に手元で原因を追えるよう $LOG は trap で消さず、最後まで残す
LOG=$(mktemp)
set +e
go test -race -timeout 60s -run TestRouter ./internal/agent/... > "$LOG" 2>&1
RUN_EXIT=$?
set -e
tail -n 200 "$LOG"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: Router tests failed (full output preserved at %s)${NC}\n" "$LOG"
  exit "$RUN_EXIT"
fi
# 成功時のみ一時ファイルを掃除する
rm -f "$LOG"
printf "${GREEN}OK: Router.Pick is deterministic and shadow ratio is capped at 0.5${NC}\n"
