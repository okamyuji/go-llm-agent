#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 04 HTTP Bearer Auth とレート制限の E2E スクリプト。
# auth 有効化した serve に対して 401 / 200 / 429 のシナリオを検証する。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"; if [[ -n "${AGENT_PID:-}" ]]; then kill "$AGENT_PID" 2>/dev/null || true; fi' EXIT

printf "${YELLOW}>>> building agent binary${NC}\n"
go build -o "$WORK/agent" ./cmd/agent

cat > "$WORK/cfg.yaml" <<EOF
default_model: openai/gpt-4o-mini
providers:
  openai:
    base_url: http://127.0.0.1:65535
    api_key_env: AGENT_DUMMY_KEY
agent:
  max_tool_hops: 1
  enabled_tools: [fs_read]
tools:
  fs:
    allow_paths: [.]
    max_read_bytes: 1024
  shell:
    timeout_seconds: 5
    allow_binaries: []
  http_fetch:
    timeout_seconds: 1
  search_files:
    max_results: 10
server:
  addr: 127.0.0.1:14004
  auth:
    enabled: true
    bearer_tokens:
      - id: local
        secret_env: AGENT_LOCAL_TOKEN
  rate_limit:
    enabled: true
    rps: 1
    burst: 1
    per_token: false
storage:
  sessions_dir: ${WORK}/sessions
logging:
  format: text
  level: warn
EOF
export AGENT_DUMMY_KEY=dummy
export AGENT_LOCAL_TOKEN=local-token-secret-1234

printf "${YELLOW}>>> starting agent serve${NC}\n"
"$WORK/agent" serve --config "$WORK/cfg.yaml" > "$WORK/serve.log" 2>&1 &
AGENT_PID=$!
SERVE_READY=0
for _ in $(seq 1 20); do
  sleep 0.2
  if curl -sf http://127.0.0.1:14004/healthz > /dev/null; then
    SERVE_READY=1
    break
  fi
done
if [[ "$SERVE_READY" -ne 1 ]]; then
  printf "${RED}FAIL: agent serve did not become healthy within 4s${NC}\n"
  cat "$WORK/serve.log"
  exit 1
fi

printf "${YELLOW}>>> /healthz should be open without auth${NC}\n"
HTTP=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:14004/healthz)
if [[ "$HTTP" != "200" ]]; then
  printf "${RED}FAIL: /healthz expected 200 got %s${NC}\n" "$HTTP"
  exit 1
fi

printf "${YELLOW}>>> /v1/models without token should be 401${NC}\n"
HTTP=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:14004/v1/models)
if [[ "$HTTP" != "401" ]]; then
  printf "${RED}FAIL: expected 401 got %s${NC}\n" "$HTTP"
  exit 1
fi

printf "${YELLOW}>>> /v1/models with wrong token should be 401${NC}\n"
HTTP=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer wrong" http://127.0.0.1:14004/v1/models)
if [[ "$HTTP" != "401" ]]; then
  printf "${RED}FAIL: expected 401 got %s${NC}\n" "$HTTP"
  exit 1
fi

printf "${YELLOW}>>> /v1/models with valid token should be 200${NC}\n"
HTTP=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $AGENT_LOCAL_TOKEN" http://127.0.0.1:14004/v1/models)
if [[ "$HTTP" != "200" ]]; then
  printf "${RED}FAIL: expected 200 got %s${NC}\n" "$HTTP"
  exit 1
fi

printf "${YELLOW}>>> burst exhaustion should yield 429${NC}\n"
# rate_limit.burst=1 (上の cfg.yaml で固定) を直前のリクエストで使い切っているため、
# AGENT_LOCAL_TOKEN を載せた即時の追加リクエストは 429 が返る想定
# CI 環境で 1 秒以上経過したケースでは bucket が充填されて 200 になり、まれに偽陽性が発生し得る
# その場合は cfg.rate_limit.rps を下げるか、ここで再現性のある時刻同期を導入する
SECOND=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $AGENT_LOCAL_TOKEN" http://127.0.0.1:14004/v1/models)
if [[ "$SECOND" != "429" ]]; then
  printf "${RED}FAIL: expected 429 after burst exhausted got %s${NC}\n" "$SECOND"
  exit 1
fi

printf "${GREEN}OK: bearer auth and rate limit enforced as expected${NC}\n"
