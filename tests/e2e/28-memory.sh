#!/usr/bin/env bash
# 28 自動メモリの E2E スクリプト。
# fixtures/memory_exercise が # プレフィックス保存 → 索引注入 → ツール往復を
# 実 LLM に依存しないスクリプト provider で検証する (docs/design/18-memory.md 8 節)。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building memory exerciser${NC}\n"
go build -o "$WORK/mem" ./tests/e2e/fixtures/memory_exercise

printf "${YELLOW}>>> running memory exerciser${NC}\n"
set +e
"$WORK/mem" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: memory exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit "$RUN_EXIT"
fi

fail=0
check_true() {
  local key="$1"
  local got
  got="$(grep -o "^${key}=.*" "$WORK/out.log" || true)"
  if [[ "$got" != "${key}=true" ]]; then
    printf "${RED}FAIL: %s want=true got=%s${NC}\n" "$key" "${got:-<missing>}"
    fail=1
  fi
}

check_true "memory_hash_saved"
check_true "memory_index_injected"
check_true "memory_tool_roundtrip"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

printf "${GREEN}OK: 自動メモリの保存・索引注入・ツール往復が動作した${NC}\n"
