#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 11 RAG MVP の E2E スクリプト。NoteStore と note_add/note_search ツールを実行する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

printf "${YELLOW}>>> building rag exerciser${NC}\n"
go build -o "$WORK/rag" ./tests/e2e/fixtures/rag_exercise

printf "${YELLOW}>>> running rag exerciser${NC}\n"
"$WORK/rag" > "$WORK/out.log" 2>&1
cat "$WORK/out.log"
if ! grep -q 'search_top=true' "$WORK/out.log"; then
  printf "${RED}FAIL: expected note_search to return the OTel note${NC}\n"
  exit 1
fi

printf "${GREEN}OK: note_add and note_search tools work against FileNoteStore${NC}\n"
