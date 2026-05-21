#!/usr/bin/env bash
# quality-gate.sh
# 品質ゲート一式。pre-commit と CI から **完全に同じコマンド** で呼び出される。
# gitleaks は --no-git --source . で作業ツリー (staged 含む) をスキャンする方式に
# 統一しており、v8.30 で protect --staged の挙動が安定しない問題を回避する。
set -euo pipefail

echo "==> gofmt"
./scripts/hooks/check_gofmt.sh

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

echo "all quality checks passed"
