#!/usr/bin/env bash
set -euo pipefail
diff=$(gofmt -l .)
if [[ -n "$diff" ]]; then
  echo "gofmt は次のファイルで差分を検出しました" >&2
  echo "$diff" >&2
  exit 1
fi
