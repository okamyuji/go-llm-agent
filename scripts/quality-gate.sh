#!/usr/bin/env bash
# quality-gate.sh
# 品質ゲート一式。pre-commit と CI から **完全に同じコマンド** で呼び出される。
# gitleaks は --no-git --source . で作業ツリー (staged 含む) をスキャンする方式に
# 統一しており、v8.30 で protect --staged の挙動が安定しない問題を回避する。
set -euo pipefail

echo "==> gofmt"
./scripts/hooks/check_gofmt.sh

echo "==> mutation-diff package filter"
bash scripts/quality/mutation-diff_test.sh

echo "==> go vet"
go vet ./...

echo "==> staticcheck"
staticcheck ./...

echo "==> golangci-lint"
golangci-lint run --timeout 5m ./...

echo "==> govulncheck"
govulncheck ./...

echo "==> go test (count=1 shuffle=on race cover)"
go test --count=1 --shuffle=on -race -cover ./...

echo "==> release build smoke (go build -o bin/agent)"
mkdir -p bin
go build -o bin/agent ./cmd/agent

echo "==> staged-secret-files-guard"
# .env / config.yaml が staged されているとローカル機密がコミットされる可能性がある。
# .gitignore に登録済みだが git add -f で強制 stage する経路を遮断する。
# CI 環境では git diff --cached が空なので no-op。
staged_sensitive=$(git diff --cached --name-only 2>/dev/null | grep -E '^(\.env|config\.yaml)$' || true)
if [ -n "$staged_sensitive" ]; then
  echo "ERROR: 以下のファイルは git にコミットしてはいけません (ローカル専用):" >&2
  echo "$staged_sensitive" >&2
  echo "  → git reset HEAD <file> で staging から外してください" >&2
  exit 2
fi

echo "==> gitleaks (detect --no-git: scans working tree including staged files)"
gitleaks detect --no-git --source . --redact --no-banner --config .gitleaks.toml

# RUN_E2E=1 のときのみ E2E スクリプトを実行する
# E2E は外部ネットワーク非依存だが、コレクター起動や複数 go build を伴うため
# pre-commit では skip し、CI と明示要求時のみ全件走らせる
if [ "${RUN_E2E:-0}" = "1" ]; then
  echo "==> e2e (28 scripts)"
  # nullglob を有効化してマッチが 0 件のときにリテラルパターンを実行しないようにする
  shopt -s nullglob
  # 実行順序を決定論的にするため bash の glob ソート (デフォルトで lexical) を明示的に期待する
  # 01- から 28- まで番号を頭に付けているため、lexical 順 = 設計書の章順序になる
  # set -e に従って最初の失敗で即抜けるため、一回の実行で複数 E2E の同時失敗を観測したい場合は
  # RUN_E2E_KEEPGOING=1 を別途設定して個別実行する運用にする
  e2e_failed=0
  for s in tests/e2e/*.sh; do
    echo "    > $s"
    if ! bash "$s"; then
      e2e_failed=1
      if [ "${RUN_E2E_KEEPGOING:-0}" != "1" ]; then
        exit 1
      fi
    fi
  done
  shopt -u nullglob
  if [ "$e2e_failed" -ne 0 ]; then
    echo "==> one or more e2e scripts failed (keep-going mode)"
    exit 1
  fi
fi

echo "all quality checks passed"
