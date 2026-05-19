#!/usr/bin/env bash
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

echo "all quality checks passed"
