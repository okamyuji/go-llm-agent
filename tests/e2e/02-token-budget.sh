#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 02 トークン使用量とコスト集計の E2E スクリプト。
# serve サブコマンドを使い /v1/usage の挙動を確認する。
# LLM への実通信は行わず、無効エンドポイントで意図的に失敗させる構成にして
# ローカル PC 固有の API キーや課金には依存しない。
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
    pricing:
      input_per_million_jpy: 450
      output_per_million_jpy: 1800
agent:
  max_tool_hops: 1
  enabled_tools: [fs_read]
  budget:
    session_max_tokens: 200000
    daily_max_cost_jpy: 1000
tools:
  fs:
    allow_paths: [.]
    deny_paths: []
    max_read_bytes: 1024
  shell:
    timeout_seconds: 5
    allow_binaries: []
  http_fetch:
    deny_private_networks: true
    timeout_seconds: 1
  search_files:
    max_results: 10
server:
  addr: 127.0.0.1:14002
storage:
  sessions_dir: ${WORK}/sessions
logging:
  format: text
  level: info
EOF
export AGENT_DUMMY_KEY=dummy

printf "${YELLOW}>>> starting agent serve in background${NC}\n"
"$WORK/agent" serve --config "$WORK/cfg.yaml" > "$WORK/serve.log" 2>&1 &
AGENT_PID=$!
# /healthz が 200 で応答するまで最大 4 秒待機する
SERVE_READY=0
for _ in $(seq 1 20); do
  sleep 0.2
  if curl -sf http://127.0.0.1:14002/healthz > /dev/null; then
    SERVE_READY=1
    break
  fi
done
if [[ "$SERVE_READY" -ne 1 ]]; then
  printf "${RED}FAIL: agent serve did not become healthy within 4s${NC}\n"
  cat "$WORK/serve.log"
  exit 1
fi

printf "${YELLOW}>>> GET /v1/usage?session=missing (no data yet)${NC}\n"
RESP=$(curl -sf http://127.0.0.1:14002/v1/usage?session=missing)
echo "$RESP"
if ! echo "$RESP" | grep -q '"scope":"session"'; then
  printf "${RED}FAIL: session scope not present in response${NC}\n"
  exit 1
fi

TODAY=$(date -u +%Y-%m-%d)
printf "${YELLOW}>>> GET /v1/usage?date=%s${NC}\n" "$TODAY"
RESP=$(curl -sf "http://127.0.0.1:14002/v1/usage?date=${TODAY}")
echo "$RESP"
if ! echo "$RESP" | grep -q '"scope":"date"'; then
  printf "${RED}FAIL: date scope not present in response${NC}\n"
  exit 1
fi

printf "${YELLOW}>>> GET /v1/usage with no params should be 400${NC}\n"
HTTP_CODE=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:14002/v1/usage)
if [[ "$HTTP_CODE" != "400" ]]; then
  printf "${RED}FAIL: expected 400 got %s${NC}\n" "$HTTP_CODE"
  exit 1
fi

printf "${YELLOW}>>> GET /v1/usage with bad date should be 400${NC}\n"
HTTP_CODE=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:14002/v1/usage?date=invalid")
if [[ "$HTTP_CODE" != "400" ]]; then
  printf "${RED}FAIL: expected 400 got %s${NC}\n" "$HTTP_CODE"
  exit 1
fi

printf "${GREEN}OK: /v1/usage endpoint behaves as expected${NC}\n"
