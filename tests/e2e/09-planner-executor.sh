#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 09 戦略切替の E2E スクリプト。react / planner_executor / reflection の 3 戦略と
# unknown 値の react フォールバックを確認する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building strategy exerciser${NC}\n"
go build -o "$WORK/st" ./tests/e2e/fixtures/strategy_exercise

printf "${YELLOW}>>> running strategy exerciser${NC}\n"
"$WORK/st" > "$WORK/out.log" 2>&1
cat "$WORK/out.log"
if ! grep -q 'strategies_loaded=react,planner_executor,reflection fallback=react' "$WORK/out.log"; then
  printf "${RED}FAIL: expected all three strategies loaded${NC}\n"
  exit 1
fi

printf "${GREEN}OK: react / planner_executor / reflection are selectable${NC}\n"
