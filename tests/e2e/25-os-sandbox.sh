#!/usr/bin/env bash
# Conforms to docs/specs/2026-08-13-improvements/00-overview.md 5節
# 25 OS sandbox (darwin sandbox-exec) の E2E スクリプト。darwin 以外は skip する。
#
# 設計書 08-os-sandbox.md 5.2 節からの逸脱: 仕様の ALLOWED_DIR/DENIED_DIR は両方とも
# mktemp -d (= $TMPDIR 配下) に置かれるが、buildSeatbeltProfile は $TMPDIR を
# allow_paths と無関係に常に書込み許可するため (2節)、DENIED_DIR を $TMPDIR 配下に
# 置くと OS 層の拒否を検証できない。DENIED_DIR はリポジトリ直下 (TMPDIR 外) に作る
# ことで allow_paths による制御そのものを検証する。ALLOWED_DIR は従来どおり
# mktemp -d 配下 (仕様の mktemp -d 由来パス許可も同時に確認できる)。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

if [[ "$(uname -s)" != "Darwin" ]]; then
  printf "${YELLOW}SKIP: os_sandbox は darwin 限定のため skip${NC}\n"
  exit 0
fi

WORK="$(mktemp -d)"
ALLOWED_DIR="$WORK/allowed"
mkdir -p "$ALLOWED_DIR"
DENIED_DIR="$ROOT/.e2e-os-sandbox-denied-$$"
mkdir -p "$DENIED_DIR"
trap 'rm -rf "$WORK" "$DENIED_DIR"' EXIT

printf "${YELLOW}>>> building os sandbox exerciser${NC}\n"
go build -o "$WORK/ossb" ./tests/e2e/fixtures/os_sandbox_exercise

printf "${YELLOW}>>> running os sandbox exerciser${NC}\n"
set +e
"$WORK/ossb" -allow "$ALLOWED_DIR" -denied "$DENIED_DIR" > "$WORK/out.log" 2>&1
RUN_EXIT=$?
set -e
cat "$WORK/out.log"
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: os sandbox exerciser exited with %d${NC}\n" "$RUN_EXIT"
  exit "$RUN_EXIT"
fi
if ! grep -q 'write_to_allowed_ok=true' "$WORK/out.log"; then
  printf "${RED}FAIL: expected write_to_allowed_ok=true${NC}\n"
  exit 1
fi
if ! grep -q 'write_to_denied_blocked=true' "$WORK/out.log"; then
  printf "${RED}FAIL: expected write_to_denied_blocked=true (OS 層で拒否されること)${NC}\n"
  exit 1
fi
if [[ -f "$DENIED_DIR/should_not_exist" ]]; then
  printf "${RED}FAIL: denied ディレクトリにファイルが作成されてしまった${NC}\n"
  exit 1
fi

printf "${GREEN}OK: sandbox-exec が allow_paths 外への書き込みを OS 層で拒否した${NC}\n"
