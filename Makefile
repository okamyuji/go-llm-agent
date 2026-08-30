SHELL := /bin/bash
GO ?= go
# 未指定なら直近の v タグ基準 (例: v0.13.0-3-gabc1234-dirty)。タグが無ければ dev
VERSION ?= $(shell git describe --tags --always --dirty --match 'v[0-9]*' 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PKG := ./...
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build build-all test lint vuln quality secrets-scan precommit-install run

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/agent ./cmd/agent

build-all:
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
	  os=$$(echo $$p | cut -d/ -f1); arch=$$(echo $$p | cut -d/ -f2); \
	  ext=""; if [ $$os = windows ]; then ext=.exe; fi; \
	  out="dist/agent-$$os-$$arch$$ext"; \
	  echo ">> $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/agent || exit 1; \
	done

test:
	$(GO) test --count=1 --shuffle=on -race -cover $(PKG)

lint:
	$(GO) vet $(PKG)
	staticcheck $(PKG)
	golangci-lint run --timeout 5m $(PKG)

vuln:
	govulncheck $(PKG)

quality:
	./scripts/quality-gate.sh

secrets-scan:
	gitleaks detect --redact --verbose --config .gitleaks.toml --no-banner

precommit-install:
	pre-commit install

run:
	./bin/agent chat
