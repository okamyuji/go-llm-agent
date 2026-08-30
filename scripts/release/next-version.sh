#!/usr/bin/env bash
# next-version.sh — 直近の v タグと Conventional Commits から次の semantic version を計算する。
# 使い方: scripts/release/next-version.sh [ref]
#   ref (既定 HEAD) までのコミットを対象にし、次のバージョン (vX.Y.Z) を標準出力へ 1 行出す。
#   直近タグ以降にコミットが無ければ直近タグをそのまま出す (bump 無し)。
#
# bump 規則:
#   - subject が `type(scope)!:` 形式、または本文に `BREAKING CHANGE` → MAJOR
#     (ただし MAJOR が 0 の間は 0.x 系の慣例に従い MINOR を上げる)
#   - subject が `feat` → MINOR
#   - それ以外 (fix / refactor / docs / chore / test / ci / perf / style) → PATCH
#
# タグが 1 つも無い場合は「マージ済み PR の本数」を MINOR に置いた v0.<PR本数>.0 を起点とする
# (gh CLI が使える環境のみ。使えなければ v0.0.0 を起点にする)。
set -euo pipefail

REF="${1:-HEAD}"

latest_tag() {
  git describe --tags --abbrev=0 --match 'v[0-9]*' "$REF" 2>/dev/null || true
}

baseline_without_tag() {
  local count=0
  if command -v gh >/dev/null 2>&1; then
    count="$(gh pr list --state merged --limit 1000 --json number --jq 'length' 2>/dev/null || echo 0)"
  fi
  echo "v0.${count}.0"
}

# bump_kind <range> → major | minor | patch
bump_kind() {
  local range="$1"
  local subjects bodies
  subjects="$(git log --format='%s' "$range")"
  bodies="$(git log --format='%b' "$range")"
  if grep -Eq '^[a-z]+(\([^)]*\))?!:' <<<"$subjects" || grep -q 'BREAKING CHANGE' <<<"$bodies"; then
    echo major
  elif grep -Eq '^feat(\([^)]*\))?:' <<<"$subjects"; then
    echo minor
  else
    echo patch
  fi
}

bump() {
  local current="$1" kind="$2"
  local major minor patch
  IFS=. read -r major minor patch <<<"${current#v}"
  case "$kind" in
    major)
      if [ "$major" -eq 0 ]; then
        minor=$((minor + 1)); patch=0
      else
        major=$((major + 1)); minor=0; patch=0
      fi
      ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    patch) patch=$((patch + 1)) ;;
  esac
  echo "v${major}.${minor}.${patch}"
}

tag="$(latest_tag)"
if [ -z "$tag" ]; then
  base="$(baseline_without_tag)"
  range="$REF"
  # タグ無しの初回は起点そのものを付与する (履歴全体の bump は行わない)
  echo "$base"
  exit 0
fi

if [ "$(git rev-list --count "${tag}..${REF}")" -eq 0 ]; then
  echo "$tag"
  exit 0
fi

bump "$tag" "$(bump_kind "${tag}..${REF}")"
