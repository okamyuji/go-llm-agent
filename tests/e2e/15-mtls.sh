#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 15 mTLS と OAuth2 の E2E スクリプト。BuildTLSConfig と NewJWTVerifier の
# 各分岐を -race 付きで検証する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

printf "${YELLOW}>>> running TestBuildTLSConfig_* / TestResolveMinVersion / TestNewJWTVerifier_*${NC}\n"
go test -race -run 'TestBuildTLSConfig|TestResolveMinVersion|TestNewJWTVerifier' ./internal/transport/httpapi/... 2>&1 | tail -5
RUN_EXIT=$?
if [[ "$RUN_EXIT" -ne 0 ]]; then
  printf "${RED}FAIL: mTLS / OAuth2 tests failed${NC}\n"
  exit 1
fi
printf "${GREEN}OK: TLS config building and JWT verifier construction behave as expected${NC}\n"
