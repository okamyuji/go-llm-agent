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
  # 14004 は他の E2E スクリプトとぶつからない範囲で固定割り当て
  # 並行実行で衝突する場合は環境変数 AGENT_HTTP_PORT を読む形に切り替える
  addr: 127.0.0.1:14004
  auth:
    enabled: true
    bearer_tokens:
      - id: local
        secret_env: AGENT_LOCAL_TOKEN
  rate_limit:
    enabled: true
    # rps/burst を低めに設定して burst exhaustion を検証するが、
    # auth テストが直前で消費するクォータを考慮して burst を 4 に確保する
    # (RateLimit を Auth より外に置く middleware 順序になったため)
    rps: 4
    burst: 4
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
# burst=4 を確実に使い切るため、まず無条件で 8 件送って token bucket を強制 drain する
# その後の連打で 1 件以上 429 を観測すれば PASS とする
# (CI 環境で rps の補充が早くてもこの確認は安定する)
for _ in 1 2 3 4 5 6 7 8; do
  curl -s -o /dev/null -H "Authorization: Bearer $AGENT_LOCAL_TOKEN" http://127.0.0.1:14004/v1/models || true
done
GOT_429=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $AGENT_LOCAL_TOKEN" http://127.0.0.1:14004/v1/models)
  if [[ "$CODE" == "429" ]]; then
    GOT_429=1
    break
  fi
done
if [[ "$GOT_429" -ne 1 ]]; then
  printf "${RED}FAIL: drain 後の 10 連続リクエストで 429 を 1 度も観測できませんでした${NC}\n"
  exit 1
fi

printf "${GREEN}OK: bearer auth and rate limit enforced as expected${NC}\n"
