#!/usr/bin/env bash
# next-version.sh の bump 規則を一時 git リポジトリで検証する。
# 使い方: bash scripts/release/next-version_test.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT="$ROOT/scripts/release/next-version.sh"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail=0
expect() {
  local want="$1" got="$2" label="$3"
  if [ "$want" != "$got" ]; then
    echo "FAIL: $label want=$want got=$got" >&2
    fail=1
  else
    echo "ok: $label -> $got"
  fi
}

# gh が無い前提で起点計算を固定する (PATH から gh を外す)
export PATH="$WORK/bin:/usr/bin:/bin"
mkdir -p "$WORK/bin"

cd "$WORK"
git init -q repo && cd repo
git config user.email t@example.com
git config user.name t
# 利用者のグローバル設定 (タグ署名・強制注釈) に影響されないよう軽量タグで固定する
git config tag.gpgSign false
git config tag.forceSignAnnotated false
git config commit.gpgSign false
git commit -q --allow-empty -m "chore: init"

expect "v0.0.0" "$(bash "$SCRIPT")" "タグ無し・gh 無しは v0.0.0 を起点にする"

git tag v0.13.0
expect "v0.13.0" "$(bash "$SCRIPT")" "タグ以降にコミットが無ければそのまま"

git commit -q --allow-empty -m "fix(agent): something"
expect "v0.13.1" "$(bash "$SCRIPT")" "fix は PATCH"

git commit -q --allow-empty -m "docs: note"
expect "v0.13.1" "$(bash "$SCRIPT")" "docs は PATCH (累積しても 1 段)"

git commit -q --allow-empty -m "feat(memory): add store"
expect "v0.14.0" "$(bash "$SCRIPT")" "feat は MINOR"

git commit -q --allow-empty -m "feat!: breaking"
expect "v0.14.0" "$(bash "$SCRIPT")" "0.x 系の breaking は MINOR 扱い"

git tag v1.2.3
git commit -q --allow-empty -m "refactor: x" -m "BREAKING CHANGE: api"
expect "v2.0.0" "$(bash "$SCRIPT")" "1.x 以降の BREAKING CHANGE は MAJOR"

git tag v2.0.0
git commit -q --allow-empty -m "perf(loop): faster"
expect "v2.0.1" "$(bash "$SCRIPT")" "perf は PATCH"

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "all next-version checks passed"
