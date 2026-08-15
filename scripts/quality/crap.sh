#!/usr/bin/env bash
# crap.sh — 変更関数の CRAP スコアを算出する
# CRAP(m) = comp(m)^2 * (1 - cov(m))^3 + comp(m)
# 使い方: scripts/quality/crap.sh <base-ref> [threshold]
#   base-ref との diff で変更された関数のみを判定する。threshold 既定 15。
set -euo pipefail

BASE="${1:?usage: crap.sh <base-ref> [threshold]}"
THRESHOLD="${2:-15}"
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# 変更された .go ファイル (テスト以外)。
# lineedit/terminal.go は x/term からのフォーク基底 (第三者由来コード) のため
# ゲート対象外 (docs/specs/2026-08-13-improvements/00-overview.md 6 章の凍結事項)
git diff --name-only "$BASE"...HEAD -- '*.go' | grep -v '_test\.go$' | grep -v 'lineedit/terminal\.go$' > "$TMP/changed_files" || true
if [ ! -s "$TMP/changed_files" ]; then
  echo "no changed non-test go files"
  exit 0
fi

# 変更ファイルが属するパッケージのカバレッジを取得
PKGS=$(while read -r f; do dirname "$f"; done < "$TMP/changed_files" | sort -u | sed 's|^|./|' | tr '\n' ' ')
# shellcheck disable=SC2086
go test -coverprofile="$TMP/cover.out" $PKGS > /dev/null
go tool cover -func="$TMP/cover.out" > "$TMP/funccov"

# 変更行の範囲を関数に対応付けるため、変更ファイルの関数別 complexity を取得
# shellcheck disable=SC2046
gocyclo $(cat "$TMP/changed_files" | tr '\n' ' ') > "$TMP/cyclo" || true

# 変更行番号の抽出 (追加行のみで十分。削除のみの関数は対象外とする)
git diff --unified=0 "$BASE"...HEAD -- '*.go' | awk '
  /^\+\+\+ b\// { file = substr($2, 3) }
  /^@@/ {
    split($3, a, ",");
    start = substr(a[1], 2) + 0;
    len = (a[2] == "" ? 1 : a[2] + 0);
    if (len > 0 && file !~ /_test\.go$/) print file ":" start ":" start + len - 1;
  }
' > "$TMP/changed_ranges"

fail=0
echo "function                                                        comp   cov%   CRAP"
while IFS= read -r line; do
  # gocyclo 形式: "<comp> <pkg> <func> <file>:<line>:<col>"
  comp=$(echo "$line" | awk '{print $1}')
  fn=$(echo "$line" | awk '{print $3}')
  loc=$(echo "$line" | awk '{print $4}')
  file="${loc%%:*}"
  fline=$(echo "$loc" | cut -d: -f2)

  # この関数が変更範囲と重なるか (関数開始行から次の関数までを近似: 開始行±0 で判定せず、
  # 変更範囲の開始行が関数開始行以上で、かつ同ファイル内の次関数開始行未満)
  next=$(awk -v f="$file" -v cur="$fline" '$4 ~ "^"f":" { split($4,p,":"); if (p[2]+0 > cur+0) print p[2] }' "$TMP/cyclo" | sort -n | head -1)
  [ -z "$next" ] && next=999999
  touched=$(awk -F: -v f="$file" -v s="$fline" -v e="$next" '$1 == f && $3+0 >= s+0 && $2+0 < e+0 { print "yes"; exit }' "$TMP/changed_ranges")
  [ "$touched" != "yes" ] && continue

  # go tool cover -func から該当関数のカバレッジ取得。
  # gocyclo はメソッドを "(*T).Name" と出すが cover -func は "Name" しか出さないため
  # 名前では突き合わせられない。双方が出す "ファイル:宣言行" を鍵にする
  # (同一ファイルに同名メソッドが複数ある場合に取り違えないため)。
  # cover -func の第 1 列はモジュールパス付きなので後方一致で見る
  cov=$(awk -v key="$file:$fline:" 'index($1, key) > 0 { gsub(/%/, "", $3); print $3 }' "$TMP/funccov" | head -1)
  [ -z "$cov" ] && cov=0
  crap=$(awk -v c="$comp" -v v="$cov" 'BEGIN { cv = v / 100; printf "%.1f", c*c*(1-cv)^3 + c }')
  over=$(awk -v x="$crap" -v t="$THRESHOLD" 'BEGIN { print (x > t) ? 1 : 0 }')
  printf "%-62s %5s %6s %6s %s\n" "$file:$fn" "$comp" "$cov" "$crap" "$([ "$over" = "1" ] && echo 'OVER')"
  [ "$over" = "1" ] && fail=1
done < "$TMP/cyclo"

if [ "$fail" = "1" ]; then
  echo "FAIL: CRAP > $THRESHOLD の変更関数があります"
  exit 1
fi
echo "OK: 全変更関数 CRAP <= $THRESHOLD"
