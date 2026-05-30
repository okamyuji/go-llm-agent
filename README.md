# go-llm-agent

Go 1.25製のCGOなし単一バイナリAIエージェントです。OpenAI、Anthropic、Google Gemini、Ollamaを同一CLIとHTTP APIから扱えます。LiteLLMのように複数のLLMプロバイダーを統一インターフェースで操作でき、その上に薄いエージェントループ (tool calling、会話履歴、内蔵ツール) を提供します。

## 主な特徴

- 単一バイナリで配布できます。CGO不要で Linux、macOS、Windowsのamd64とarm64に対応します
- 4プロバイダー (OpenAI、Anthropic、Google Gemini、Ollama) を統一した抽象層として扱えます
- ストリーミングとtool callingを初期サポートします
- 内蔵ツールはfs_read、fs_write、shell、http_fetch、search_filesの5種類です
- 対話REPL、ワンショットrun、OpenAI互換HTTP APIの3種類のインターフェースを提供します
- pre-commitとCIでgofmt、go vet、staticcheck、golangci-lint、govulncheck、go test --count=1 --shuffle=on、gitleaksを全部通します

## クイックスタート

```bash
cp .env.example .env
$EDITOR .env                 # APIキーなどの実値を入れてください

cp config.yaml.example config.yaml
$EDITOR config.yaml          # allow_pathsなどを必要に応じて編集

make build
./bin/agent chat --model gemini/gemini-2.5-pro
```

> `config.yaml` は個人ローカル設定として `.gitignore` 対象です。リポジトリには安全寄りの `config.yaml.example` のみがコミットされます。Anthropic/Claude のキーは `ANTHROPIC_API_KEY` を基本名とし、CLI は互換名として `CLAUDE_API_KEY` も試します。設定例の完全な内容は `config.yaml.example` を参照してください。

## セキュリティ

このエージェントはモデル出力に駆動されるため、以下の多層的なガードを設計しています。
詳細は `config.yaml.example` のコメントも参照してください。

### デフォルトは readonly

`agent.enabled_tools` を空または未指定にすると、Registryは`fs_read` / `search_files` / `http_fetch` の **readonly セット**のみを有効にします(`tool.DefaultReadonlyTools`)。
`fs_write` と `shell` を有効にする場合は意図して明示列挙してください。

### サンドボックスとセンシティブパス

`tool.NewSandboxWithDeny` はシンボリックリンク解決済みのパスを `allow_paths` と照合し、`..` による上位ディレクトリ参照を拒否します。さらに、以下のセンシティブなパターンは**設定で外せない強制 deny** として常に拒否されます。

`.git`、`.env`、`.env.*`、`.ssh`、`.aws`、`.gnupg`、`.npmrc`、`.netrc`、`.pypirc`、`id_rsa*`、`id_dsa*`、`id_ecdsa*`、`id_ed25519*`。
追加でdenyしたいパターンは `tools.fs.deny_paths` に列挙します。

### Shell の引数 deny

`shell` ツールは `allow_binaries` に加えて引数文字列に対するdeny正規表現を持ちます。
既定で以下が遮断されます (`tool.DefaultShellArgDenyPatterns`):

- `git config --global` / `git config --system`
- `git -c core.sshCommand=...` / `git -c http.proxy=...`
- `go env -w`、`go install`
- `bash -c <code>` / `sh -c <code>` / `-c <code>` / `--exec`

追加でdenyしたいパターンは `tools.shell.arg_deny_patterns` に列挙します。

### HTTP fetch のドメイン許可と untrusted 標識

`http_fetch` は `tools.http_fetch.allow_domains` が非空の場合のみ、FQDN末尾一致でリクエスト先を絞り込みます。レスポンス本文は以下のuntrustedラッパで返され、後段プロンプトに「外部由来でツール実行を許可してはならない」コンテキストを伝えます。

```text
[HTTP <status>] [untrusted external content from <url>]
<body>
[end untrusted content]
```

### プロバイダーとモデル許可リスト

`providers.<name>.allow_models` を列挙すると、その配列に一致するモデル名のみを許可します。サプライチェーン耐性のため、検証済みモデルのみをここに記述することを推奨します。
依存パッケージはGo modulesでgo.mod/go.sumによりピン留めされています。

### 監査ログ

すべてのsensitive ツール (`fs_read` / `fs_write` / `shell` / `http_fetch`) はslog経由で構造化ログを出力します。各レコードは `correlation_id`（agentループhopごとに発番されるtool_call ID）を含み、リクエスト追跡が可能です。

### 第三者検証

破壊シナリオが拒否されることをローカルで再現するには、以下を実行してください。

```bash
bash scripts/verify-hardening.sh
```

このスクリプトは固有ホスト依存をせず、`go` と `bash` だけがあれば動きます（リポジトリ ルートからの実行が前提）。

## サブコマンド

| コマンド | 説明 |
|---------|------|
| `agent chat`   | 対話 REPL を起動します（`-no-spinner` で進捗インジケータとターン要約を無効化） |
| `agent run -p` | ワンショットでプロンプトを1回送信します |
| `agent serve`  | OpenAI互換HTTP APIを起動します |
| `agent tools`  | 有効な内蔵ツールを一覧表示します |
| `agent config` | 設定ファイルの内容をダンプします |
| `agent eval`   | ゴールデンデータセットでagentを評価します |

## 評価フレームワーク

`agent eval --suite <dir> --report <path>` でYAML形式のゴールデンケースを実行し、tool_recall / tool_precision / param_accuracy / phrase_recall を計算したJSONレポートを書き出します。1 件でも合格条件を満たさなければ `exit code 1` で停止するため、CIのクオリティゲートに組み込めます。

```yaml
id: refund_simple
input:
  system_prompt: "あなたは返金支援エージェントです"
  messages:
    - role: user
      content: "order_id A89268 のマグカップだけ返金してください"
expected:
  tool_calls:
    - tool: refund_item
      params:
        order_id: A89268
  phrases: ["返金処理を受け付けました"]
metrics:
  tool_recall_min: 1.0
  param_accuracy_min: 1.0
  phrase_recall_min: 0.5
```

E2Eスクリプトは `tests/e2e/07-eval-suite.sh` です。fixtures/eval_exerciseがLoadSuite / Score / WriteReportの動作を検証します。設計の詳細は `docs/design/07-eval-framework.md` を参照してください。

## ローカルノート (RAG MVP)

`note_add` と `note_search` の2つの内蔵ツールでJSONLベースのローカルノートを操作できます。スコアは title=3 / tags=2 / body=1 の重みで計算し、上位 `top_k` 件を返します。ノートは `storage.notes_path` (空なら `sessions_dir/notes.jsonl`) に追記します。`memory.NoteStore` インターフェースを介すため、将来SQLite FTS5やベクターDBに差し替え可能です。

E2Eスクリプトは `tests/e2e/11-rag-mvp.sh` で、fixtures/rag_exerciseが2件のノートを保存して全文検索が機能することを確認します。設計の詳細は `docs/design/11-rag-mvp.md` を参照してください。

## プロンプトテンプレート版管理

`internal/prompt` パッケージで `<name>@<version>.tmpl` 形式のテンプレートをファイルからロードできます。`Renderer` は `text/template` の安全なサブセットを使い、許可リスト外の変数キーや欠落キーはrenderエラーとして弾きます。OTel spanの `prompt.version` 属性に乗せてA/B比較する想定です。

E2Eスクリプトは `tests/e2e/13-prompt-template.sh` で TestLoader_* と TestRenderer_* を -race付きで実行します。設計の詳細は `docs/design/13-prompt-template-versioning.md` を参照してください。

## MCP クライアント

`internal/mcp.Client` で Model Context Protocol のstdio JSON-RPCサーバに接続し、`tools/list` でメソッドを発見し `tools/call` で実行できます。SSE transportは今後の拡張点で、現状はstdioのみサポートします。

```go
c, err := mcp.NewStdioClient(ctx, []string{"./mcp/docs_server"})
tools, _ := c.ListTools(ctx)
res, _ := c.Call(ctx, "search_docs", json.RawMessage(`{"query":"x"}`))
```

E2Eスクリプトは `tests/e2e/12-mcp-discovery.sh` です。`tests/e2e/fixtures/mcp_echo_server` を子プロセスで起動して JSON-RPCハンドシェイクを確認します。設計の詳細は `docs/design/12-mcp-client.md` を参照してください。

## 並列ツール実行

`service.ExecuteToolsParallel` で複数ToolCallをsemaphore付きで並列実行できます。`require_approval` 対象のツールが1件でも含まれる場合は自動的に直列化 (バリア方式) し、人間オペレータの状況把握を優先します。`fail_fast=true` のときは最初の失敗で他の実行をキャンセルします。

```yaml
agent:
  parallel_tools:
    enabled: true
    max_concurrency: 4
    fail_fast: false
```

E2Eスクリプトは `tests/e2e/10-parallel-tools.sh` で、`go test -race` 付きで `TestExecuteToolsParallel_*` を実行し、ゴルーチン競合や順序ずれが無いことを観測します。設計の詳細は `docs/design/10-parallel-tool-calls.md` を参照してください。

## 実行戦略の切替

`agent.strategy` で実行戦略を選べます。`react` (既定) は従来通り、`planner_executor` はシステムプロンプトに計画指示を注入して executor_modelでツール呼び出しを行い、`reflection` は self-checkヒントを差し込みます。

```yaml
agent:
  strategy: planner_executor
  planner_executor:
    planner_model: openai/gpt-4o
    executor_model: openai/gpt-4o-mini
    max_steps: 8
  reflection:
    max_iterations: 3
    trigger_consecutive_failures: 2
    trigger_hop_budget: 6
```

E2Eスクリプトは `tests/e2e/09-planner-executor.sh` です。fixtures/strategy_exerciseが3戦略とunknown値のフォールバック動作を検証します。設計の詳細は `docs/design/09-planner-executor.md` を参照してください。

## HITL ツール承認

`agent.approval.required_tools` に含まれるツールは、実行前に承認が必要になります。HTTPモードでは `/v1/runs/<runID>/approve` にJSONで `{call_id, allowed, reason, reviewer}` をPOSTすると該当の承認待ちが解放されます。timeoutを過ぎると `default_decision` (`deny` または `allow`) に従います。

```yaml
agent:
  approval:
    required_tools: [shell, fs_write]
    timeout_seconds: 30
    default_decision: deny
```

E2Eスクリプトは `tests/e2e/08-hitl-approval.sh` です。fixtures / approval_exercise がRequest / Submit / timeoutの挙動を確認します。設計の詳細は `docs/design/08-hitl-approval.md` を参照してください。

## PII 出力リダクション

`safety.pii_redactor` 設定でメール、日本語電話番号、マイナンバー、IPv4アドレスなどの個人情報パターンをagent出力のすべての経路でマスキングします。06番のOutputRedactorとChainRedactorで合成され、DeltaText / Final / ツール返却 / session 保存 / OTel span 属性のいずれにも同じマスク後文字列が乗ります。

E2Eスクリプトは `tests/e2e/14-pii-redact.sh` でTestPIIRedactor_* と TestChainRedactor_* を -race付きで実行します。設計の詳細は `docs/design/14-pii-redaction.md` を参照してください。

## プロンプトインジェクション検知と出力リダクション

`safety.input_scanner` で入力テキストに対する正規表現スキャン、`safety.output_redactor` で出力テキストに対する機微情報マスキングを行います。すべてのツール返却テキストは `[UNTRUSTED INPUT: tool=<name>]` で始まるuntrustedマーカーで包まれ、LLMにuntrustedソースであることを明示します。

```yaml
safety:
  input_scanner:
    enabled: true
    block_on_match: false
    patterns:
      - id: ignore_previous
        regex: "(?i)ignore (the )?previous instructions"
  output_redactor:
    enabled: true
    rules:
      - id: openai_key
        regex: "sk-[A-Za-z0-9]{20,}"
        replacement: "[REDACTED:OPENAI]"
```

DeltaText / Final / ツール出力のすべての経路で同じRedactorが適用されます。14番設計書で追加予定のPII Redactorは `ChainRedactor` で本Redactorの後段に組み合わせる想定です。

E2Eスクリプトは `tests/e2e/06-injection-and-redact.sh` です。fixtures/safety_exerciseがScannerとRedactorの動作を検証します。設計の詳細は `docs/design/06-input-output-filter.md` を参照してください。

## ツール呼び出しの強制度とスキーマ検証

`agent.tool_choice` でLLM のツール呼び出し挙動を制御できます。`mode` は `auto` / `required` / `none` / `tool` の 4 種類で、`tool` を指定したときは `name` に具体的なツール名を入れます。各プロバイダー (OpenAI / Anthropic / Gemini / Ollama) のネイティブなtool_choice仕様にマッピングされます。

`agent.tool_validation` で、ツール呼び出し時にLLMが生成するJSON引数を `tool.Spec.Schema` に照らして検証できます。スキーマ違反のときは `max_retries` 回までLLMに修正を促し、超過すると `EventError` で停止します。

```yaml
agent:
  tool_choice:
    mode: auto
    name: ""
  tool_validation:
    enabled: true
    max_retries: 2
```

E2Eスクリプトは `tests/e2e/05-tool-choice-validation.sh` です。fixtures/tool_choice_exercise のOpenAI互換fakeサーバが受信するペイロードに `tool_choice: required` が正しくマッピングされていることを確認します。設計の詳細は `docs/design/05-tool-choice-schema-validation.md` を参照してください。

## カナリアとシャドウデプロイ

`internal/agent.Router` でリクエスト単位のcanary振り分けとshadow実行設定を表現できます。`Pick(seed)` は決定論的で、同じseedには常に同じDecisionを返します。shadow ratioは副作用拡大を抑えるため0.5を上限としてハードキャップします。

```go
r := agent.NewRouter("openai/gpt-4o-mini", "anthropic/claude-sonnet-4-6", 0.05, "openai/gpt-4o", 0.10)
d := r.Pick(seedFromRequest)
```

E2Eスクリプトは `tests/e2e/16-canary-shadow.sh` で TestRouter_* を -race付きで実行し、ratio=0/1の境界値と0.5 cap、同一seedの決定論性を確認します。設計の詳細は `docs/design/16-canary-shadow.md` を参照してください。

## mTLS と OAuth2

`internal/transport/httpapi.BuildTLSConfig` でTLS終端とmTLSを構築できます。`ClientCAFile` を指定すると `RequireAndVerifyClientCert` でmTLSを強制します。`MinVersion` でTLSの最低バージョンも指定できます。

`JWTVerifier` はOAuth2リソースサーバの最小スタブです。MVPとしてshared_secret_env によるHS256検証だけ用意しており、JWKS fetchとRS256 / ES256検証は後続フェーズのgo-jwt統合で拡張します。

E2Eスクリプトは `tests/e2e/15-mtls.sh` でBuildTLSConfigとNewJWTVerifierの各分岐を -race付きで実行します。設計の詳細は `docs/design/15-mtls-oauth.md` を参照してください。

## HTTP API の認証とレート制限

`agent serve` のHTTP APIは既定で無認証です。本番運用や127.0.0.1以外で待ち受ける場合は、Bearer Token認証、レート制限、IP allowlist、CORSをまとめて有効化できます。

```yaml
server:
  addr: 0.0.0.0:14000
  auth:
    enabled: true
    bearer_tokens:
      - id: local
        secret_env: AGENT_LOCAL_TOKEN
  rate_limit:
    enabled: true
    rps: 5
    burst: 10
    per_token: true
  allowlist:
    cidrs: [127.0.0.1/32]
  cors:
    enabled: true
    allow_origins: [https://example.com]
    allow_methods: [GET, POST, OPTIONS]
    allow_headers: [Authorization, Content-Type]
```

トークン値は `secret_env` 経由でのみ与えられ、値の直書きは設定読込時に拒否されます。`Authorization: Bearer <value>` の `<value>` が `eyJ` で始まるときは将来のOAuth2 JWT検証ミドルウェアに委譲する想定で素通しします。`/healthz` だけは認証とレート制限を回避します。

E2Eスクリプトは `tests/e2e/04-http-auth.sh` です。401 / 200 / 429 のすべてのシナリオをローカル環境変数のみで検証します。設計の詳細は `docs/design/04-http-auth-ratelimit.md` を参照してください。

## リトライとフォールバック

`providers.<name>.retry` でリトライ設定を、`fallback_to` で別プロバイダーへの切替を指定できます。`request_timeout_seconds` は HTTPクライアント全体のタイムアウトを上書きします。

```yaml
providers:
  openai:
    request_timeout_seconds: 60
    retry:
      max_attempts: 4
      initial_backoff_ms: 200
      max_backoff_ms: 5000
      jitter_ratio: 0.2
    fallback_to: anthropic
```

リトライ対象は `llm.ProviderError.Retryable=true` の429と5xx系のみで、4xxの入力エラーやcontextのキャンセルは即座に失敗します。バックオフは指数増加でジッタを掛け、`MaxBackoff` を上限とします。リトライ試行数とフォールバック発火回数はOTel メトリクス `llm.retry.attempts` と `llm.fallback.total` に記録されます。

E2Eスクリプトは `tests/e2e/03-llm-retry.sh` です。`tests/e2e/fixtures/retry_exercise` のフェイクプロバイダーを介して 429を2回返した後成功するシナリオを検証します。設計の詳細は `docs/design/03-llm-retry-backoff.md` を参照してください。

## トークンとコストの集計

`providers.<name>.pricing` を設定すると、LLM呼び出しごとの入出力トークン数からJPYコストを算出し、セッション単位と日次単位で集計します。集計結果は `storage.sessions_dir/billing.jsonl` に追記され、`agent.Event` の `Usage` と `Cost` フィールド経由でリアルタイムに観測できます。

```yaml
providers:
  openai:
    pricing:
      input_per_million_jpy: 450
      output_per_million_jpy: 1800
agent:
  budget:
    session_max_tokens: 200000
    daily_max_cost_jpy: 1000
```

予算上限を超える呼び出しは `billing.ErrBudgetExceeded` で停止します。0は無制限の指定です。

HTTP APIには `/v1/usage` エンドポイントを追加しました。`?session=<id>` でセッション単位、`?date=YYYY-MM-DD`（UTC）で日次の集計をJSONで返します。

```bash
curl http://127.0.0.1:14000/v1/usage?session=sess-1
curl http://127.0.0.1:14000/v1/usage?date=2026-05-23
```

E2Eスクリプトは `tests/e2e/02-token-budget.sh` です。LLMへの実通信は不要で、ローカルPC固有のAPIキーや課金には依存しません。設計の詳細は `docs/design/02-token-cost-tracking.md` を参照してください。

## オブザーバビリティ

`config.yaml` の `observability.otel` セクションでOpenTelemetryのOTLP HTTP exporterを有効化できます。既定は無効で、有効化しても他機能の挙動は変わりません。

```yaml
observability:
  otel:
    enabled: true
    exporter: otlp_http
    endpoint: 127.0.0.1:4318
    insecure: true
    sample_ratio: 1.0
    service_name: go-llm-agent
    metrics_interval_seconds: 30
```

エクスポート対象は次のとおりです。

- スパン: `agent.run`、`llm.call`、`tool.execute`。親子関係がtrace上で1本につながります。
- メトリクス: `llm.tokens.input`、`llm.tokens.output`、`tool.duration_ms`、`tool.success`、`tool.failure`、`llm.retry.attempts`、`llm.fallback.total`。
- ログ: `obs.NewLogger` でラップした slog レコードに `trace_id` と `span_id` の属性が付きます。

実動作確認はリポジトリ同梱のE2Eスクリプトで再現できます。Goとbashのみで動き、ローカルPC固有の設定には依存しません。

```bash
bash tests/e2e/01-otel-trace.sh
```

このスクリプトは `tests/e2e/fixtures/otel_collector` でOTLP HTTPの `/v1/traces` と `/v1/metrics` を受け取るだけのモックを起動し、`agent run` の実行でモックにトレースとメトリクスが届くことを確認します。設計の詳細は `docs/design/01-otel-instrumentation.md` を参照してください。

## 開発者向け

```bash
make precommit-install   # pre-commit フックを有効化
make quality             # 品質ゲートをローカル実行（CI と同一フロー）
make build-all           # 6 バイナリへクロスコンパイル
```

`scripts/quality-gate.sh` は **pre-commit と CI で同一コマンド** を実行する単一エントリです。gofmt / go vet / staticcheck / golangci-lint / govulncheck / `go test
--count=1 --shuffle=on -race -cover` / `gitleaks detect --no-git --source .` を順に走らせます。
gitleaks は作業ツリーを直接スキャンする方式に統一しており、pre-commit でもステージ済み・未ステージ含む全ファイルを検査します。

## ライセンス

MIT
