# go-llm-agent

Go 1.25 製の CGO なし単一バイナリ AI エージェントです。OpenAI、Anthropic、Google Gemini、Ollama を同一 CLI と HTTP API から扱えます。litellm のように複数の LLM プロバイダーを統一インターフェースで操作でき、その上に薄いエージェントループ (tool calling、会話履歴、内蔵ツール) を提供します。

## 主な特徴

- 単一バイナリで配布できます。CGO 不要で Linux、macOS、Windows の amd64 と arm64 に対応します
- 4 プロバイダー (OpenAI、Anthropic、Google Gemini、Ollama) を統一抽象で扱えます
- ストリーミングと tool calling を初期サポートします
- 内蔵ツールは fs_read、fs_write、shell、http_fetch、search_files の 5 種類です
- 対話 REPL、ワンショット run、OpenAI 互換 HTTP API の 3 種類のインターフェースを提供します
- pre-commit と CI で gofmt、go vet、staticcheck、golangci-lint、govulncheck、go test --count=1 --shuffle=on、gitleaks を全部通します

## クイックスタート

```bash
cp .env.example .env
$EDITOR .env   # 実値を埋めてください

make build
./bin/agent chat --model openai/gpt-4.1-mini
```

リポジトリ ルートに `config.yaml` を置いてください。雛形は次の通りです。

```yaml
default_model: openai/gpt-4.1-mini

providers:
  openai:
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY
  anthropic:
    base_url: https://api.anthropic.com
    api_key_env: ANTHROPIC_API_KEY
  gemini:
    base_url: https://generativelanguage.googleapis.com/v1beta
    api_key_env: GEMINI_API_KEY
  ollama:
    base_url: http://localhost:11434

agent:
  max_tool_hops: 8
  enabled_tools: [fs_read, fs_write, shell, http_fetch, search_files]
  system_prompt: |
    あなたは慎重で正確な開発支援エージェントです。

tools:
  fs:
    allow_paths: ["./"]
    max_read_bytes: 1048576
  shell:
    timeout_seconds: 30
    max_timeout_seconds: 300
    allow_binaries: [git, go, ls, cat, head, tail, grep]
  http_fetch:
    deny_private_networks: true
    timeout_seconds: 15
    max_body_bytes: 2097152
  search_files:
    max_results: 200

server:
  addr: 127.0.0.1:14000

storage:
  sessions_dir: ~/.local/state/go-llm-agent/sessions

logging:
  format: text
  level: info
```

## サブコマンド

| コマンド | 説明 |
|---------|------|
| `agent chat`   | 対話 REPL を起動します |
| `agent run -p` | ワンショットでプロンプトを 1 回送信します |
| `agent serve`  | OpenAI 互換 HTTP API を起動します |
| `agent tools`  | 有効な内蔵ツールを一覧表示します |
| `agent config` | 設定ファイルの内容をダンプします |

## 開発者向け

```bash
make precommit-install   # pre-commit フックを有効化
make quality             # 品質ゲートをローカル実行
make build-all           # 6 バイナリへクロスコンパイル
```

## ライセンス

MIT
