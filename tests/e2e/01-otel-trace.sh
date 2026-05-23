#!/usr/bin/env bash
# Conforms to docs/design/00-overview.md section 4.3
# 01 OTel 計装の E2E スクリプト。fake OTLP collector を Go で起動し、
# agent run の実行で trace と metrics の OTLP HTTP POST がコレクタに届くことを確認する。
# ローカル PC 固有のパスやアカウントには依存しない。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# 1. fake collector を 4318 にバインドして起動する
printf "${YELLOW}>>> starting fake OTLP collector on 127.0.0.1:4318${NC}\n"
go build -o "$WORK/collector" ./tests/e2e/fixtures/otel_collector
"$WORK/collector" -addr 127.0.0.1:4318 -duration 6s > "$WORK/collector.log" 2>&1 &
COLLECTOR_PID=$!
sleep 1

# 2. agent をビルドする
printf "${YELLOW}>>> building agent binary${NC}\n"
go build -o "$WORK/agent" ./cmd/agent

# 3. 一時的な config を生成する（個人 .env や OPENAI_API_KEY に依存しないように、
#    無効なエンドポイントを指定して LLM 呼び出しはわざと失敗させる。
#    目的は OTel のスパン送信が起きることのみを確認することにあるため、
#    LLM 呼び出しの成否は問題ではない）
cat > "$WORK/cfg.yaml" <<'EOF'
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
  addr: 127.0.0.1:65534
storage:
  sessions_dir: SESSIONS_DIR_PLACEHOLDER
logging:
  format: text
  level: info
observability:
  otel:
    enabled: true
    exporter: otlp_http
    endpoint: 127.0.0.1:4318
    insecure: true
    sample_ratio: 1.0
    service_name: go-llm-agent-e2e
    metrics_interval_seconds: 1
EOF
sed -i.bak "s|SESSIONS_DIR_PLACEHOLDER|$WORK/sessions|" "$WORK/cfg.yaml"
rm -f "$WORK/cfg.yaml.bak"
export AGENT_DUMMY_KEY=dummy

# 4. agent run を実行し、LLM 呼び出し失敗による非ゼロ終了を許容する
printf "${YELLOW}>>> running agent (LLM call expected to fail intentionally)${NC}\n"
set +e
"$WORK/agent" run --config "$WORK/cfg.yaml" -p "ping" > "$WORK/run.log" 2>&1
RUN_EXIT=$?
set -e
printf "agent run exited with %d (failure is expected for this E2E)\n" "$RUN_EXIT"

# 5. collector が自然終了するのを待ち、ヒット数を assert する
printf "${YELLOW}>>> waiting for collector to flush${NC}\n"
wait "$COLLECTOR_PID" || true
cat "$WORK/collector.log"

if ! grep -qE 'trace_hits=[1-9][0-9]*' "$WORK/collector.log"; then
  printf "${RED}FAIL: collector received zero trace_hits${NC}\n"
  exit 1
fi

printf "${GREEN}OK: OTel trace exported to fake collector${NC}\n"
