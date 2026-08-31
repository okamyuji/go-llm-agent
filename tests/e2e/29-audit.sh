#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 29 監査イベント (audit) E2E スクリプト。
# tests/e2e/fixtures/audit_exercise を実行し、実 agent バイナリを通した
# run / serve(ヘッダあり・なし) / Iggy 不達 / IGGY_PAT 未設定 の各導線で
# 監査イベントが欠落しないことを確認する。
# ローカル PC 固有の API キーや課金には依存しない。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building audit exerciser${NC}\n"
go build -o "$WORK/audit-exercise" ./tests/e2e/fixtures/audit_exercise

printf "${YELLOW}>>> running audit exerciser${NC}\n"
set +e
"$WORK/audit-exercise" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"

if grep -qE '^FAIL' "$WORK/out.log"; then
  printf "${RED}FAIL: one or more audit flows failed${NC}\n"
  exit 1
fi
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: audit exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit 1
fi

for flow in run serve iggy_down iggy_recovery iggy_pat_unset; do
  if ! grep -qE "^PASS ${flow}\$" "$WORK/out.log"; then
    printf "${RED}FAIL: expected PASS %s in output${NC}\n" "$flow"
    exit 1
  fi
done
if ! grep -qE '^SKIP chat_compact' "$WORK/out.log"; then
  printf "${RED}FAIL: expected SKIP chat_compact in output${NC}\n"
  exit 1
fi

printf "${GREEN}OK: audit events survived run/serve/iggy-outage/no-pat flows without loss${NC}\n"
