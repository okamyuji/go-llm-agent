#!/usr/bin/env bash
# verify-hardening.sh
# Codex adversarial review 指摘に対応したセキュリティハードニングが
# 期待通り破壊シナリオを拒否することをローカルで再現確認する。
# 依存: go と bash のみ。固有ホスト/個人パスへの依存なし。
# 実行方法: リポジトリ ルートから `bash scripts/verify-hardening.sh`

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

YELLOW='\033[33m'
GREEN='\033[32m'
RED='\033[31m'
NC='\033[0m'

pass=0
fail=0

check() {
  local name="$1"
  shift
  printf "${YELLOW}>>> %s${NC}\n" "$name"
  if "$@"; then
    printf "${GREEN}    PASS${NC}\n"
    pass=$((pass+1))
  else
    printf "${RED}    FAIL${NC}\n"
    fail=$((fail+1))
  fi
}

# 前提チェック
command -v go >/dev/null 2>&1 || { echo "go not found"; exit 2; }
command -v bash >/dev/null 2>&1 || { echo "bash not found"; exit 2; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
WS="$WORK/workspace"
mkdir -p "$WS"

# テスト用の設定ファイルを一時生成（リポジトリ内固定ファイルには触れない）
cat > "$WORK/config.yaml" <<EOF
default_model: gemini/gemini-2.5-pro
providers:
  ollama:
    base_url: http://127.0.0.1:65535
agent:
  max_tool_hops: 1
  enabled_tools: [fs_read, fs_write, shell, http_fetch, search_files]
tools:
  fs:
    allow_paths: ["$WS"]
    max_read_bytes: 1024
  shell:
    timeout_seconds: 5
    max_timeout_seconds: 10
    allow_binaries: [echo, git, bash, go]
  http_fetch:
    deny_private_networks: true
    timeout_seconds: 5
    max_body_bytes: 1024
    allow_domains: [example.com]
  search_files:
    max_results: 10
server:
  addr: 127.0.0.1:0
storage:
  sessions_dir: "$WORK/sessions"
logging:
  format: text
  level: warn
EOF

# 1) 単体テスト一式
check "unit tests (go test -race ./...)" bash -c "go test -race -count=1 ./... >/dev/null"

# 2) ビルド
BIN="$WORK/agent"
check "build agent binary" bash -c "go build -o '$BIN' ./cmd/agent"

# 3) tools コマンドで設定通りのツール一覧が出ること
check "agent tools lists enabled tools" bash -c "'$BIN' tools --config '$WORK/config.yaml' | grep -q fs_read"

# 4) サンドボックス deny の単体テスト経由検証
#    sandbox の test を直接走らせてセンシティブパス deny が機能することを確認
check "sandbox sensitive path deny tests" bash -c "go test -run 'TestSandbox_Deny|TestSandbox_TraversalRejected|TestSandbox_SymlinkEscape' -count=1 ./internal/tool/... >/dev/null"

# 5) shell 引数 deny の単体テスト経由検証
check "shell arg deny tests" bash -c "go test -run TestShell_ArgDeny -count=1 ./internal/tool/... >/dev/null"

# 6) http_fetch ドメイン許可と untrusted ラッパの単体テスト経由検証
check "http_fetch domain allowlist + untrusted wrapping" bash -c "go test -run 'TestHTTPFetch_Domain|TestHTTPFetch_Untrusted' -count=1 ./internal/tool/... >/dev/null"

# 7) Registry default readonly の検証
check "registry default readonly" bash -c "go test -run TestRegistry_DefaultsReadonly -count=1 ./internal/tool/... >/dev/null"

# 8) Provider モデル許可リストの検証
check "provider allow_models" bash -c "go test -run TestRegistry_AllowModels -count=1 ./internal/llm/... >/dev/null"

# サマリ
echo
echo "----------------------------------------"
printf "passed: ${GREEN}%d${NC}  failed: ${RED}%d${NC}\n" "$pass" "$fail"
echo "----------------------------------------"
if [ "$fail" -gt 0 ]; then
  exit 1
fi
