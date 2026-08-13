#!/usr/bin/env bash
# mutation-diff.sh — gremlins の結果を git diff の変更行と突合し、
# 変更行上の生存 mutant (LIVED / NOT COVERED) があれば FAIL する。
# 使い方: scripts/quality/mutation-diff.sh <base-ref> <pkg> [pkg...]
set -euo pipefail

BASE="${1:?usage: mutation-diff.sh <base-ref> <pkg> [pkg...]}"
shift
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# 変更行 (追加行) を file:line 形式で列挙
git diff --unified=0 "$BASE"...HEAD -- '*.go' | awk '
  /^\+\+\+ b\// { file = substr($2, 3) }
  /^@@/ {
    split($3, a, ",");
    start = substr(a[1], 2) + 0;
    len = (a[2] == "" ? 1 : a[2] + 0);
    if (file !~ /_test\.go$/) for (i = 0; i < len; i++) print file ":" start + i;
  }
' | sort -u > "$TMP/changed_lines"

if [ ! -s "$TMP/changed_lines" ]; then
  echo "no changed lines"
  exit 0
fi

fail=0
for pkg in "$@"; do
  echo ">>> gremlins unleash $pkg"
  # timeout-coefficient: 既定係数では重いパッケージの mutant が全件 TIMED OUT になる
  gremlins unleash "$pkg" --timeout-coefficient 20 --workers 4 2>&1 | tee "$TMP/gremlins_out" | tail -5
  # 出力形式: "  KILLED CONDITIONALS_NEGATION at fs.go:55:41" (パスはpkg相対)
  awk -v pkg="$pkg" '
    / (LIVED|NOT COVERED) / {
      for (i = 1; i <= NF; i++) if ($i == "at") loc = $(i+1);
      split(loc, p, ":");
      print pkg "/" p[1] ":" p[2] " " $0;
    }
  ' "$TMP/gremlins_out" | sed 's|^\./||' > "$TMP/survived"

  while IFS= read -r s; do
    [ -z "$s" ] && continue
    fl="${s%% *}"
    if grep -qxF "$fl" "$TMP/changed_lines"; then
      echo "SURVIVED ON CHANGED LINE: $s"
      fail=1
    fi
  done < "$TMP/survived"
done

if [ "$fail" = "1" ]; then
  echo "FAIL: 変更行に生存 mutant があります"
  exit 1
fi
echo "OK: 変更行の mutant は全て killed"
