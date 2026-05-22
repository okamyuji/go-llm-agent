# 01. OpenTelemetry トレースとメトリクス計装

## 1. 概要

agent.Run、LLM プロバイダー呼び出し、ツール実行の各境界に OpenTelemetry のスパンとメトリクスを差し込み、トレース ID とメトリクスを OTLP エクスポータ経由で外部に転送できるようにします。

## 2. 書籍根拠

Building Applications with AI Agents, Ch10 「Monitoring Stacks」「OTel Instrumentation」「Visualization and Alerting」を参照します。書籍は agent ノードに span を張り、token 数とレイテンシを attribute に乗せる例を示しており、Grafana や Langfuse の OTLP 受信側に流す前提です。

## 3. 現状分析

`internal/obs/log.go` は slog のみで OpenTelemetry の依存を持ちません。`internal/agent/loop.go` の各ホップ、`internal/llm/<provider>/client.go` の HTTP 呼び出し、`internal/tool/*.go` の Execute 実装に span がありません。相関 ID は監査ログに含まれていますが trace_id ではないため Grafana 等で連携できません。

## 4. ゴール

- agent.Run のライフサイクル、LLM 呼び出し、ツール実行に対してスパンが連鎖して張られます。
- OTLP HTTP エクスポータが設定で有効化できます。
- token 入出力、レイテンシ、ツール成否、retry 回数がメトリクスとして取得できます。
- 既存ログにも trace_id と span_id が付加されます。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/obs/otel.go` に Tracer と MeterProvider の初期化処理を新設します。`InitTelemetry(ctx, cfg)` は shutdown 関数を返し、main で defer 解放します。トレーサ名は `go-llm-agent` です。

各層は次のヘルパを呼びます。

- agent: `obs.StartAgentSpan(ctx, model)` → span を返す。
- llm: provider 内で `obs.StartLLMSpan(ctx, providerName, model)` を呼ぶ。
- tool: `obs.StartToolSpan(ctx, toolName, callID)` を tool.Registry のラッパーで自動付与する。

### 5.2 設定スキーマ

`config.yaml` に次を追加します。

```yaml
observability:
  otel:
    enabled: false
    exporter: otlp_http
    endpoint: http://127.0.0.1:4318
    insecure: true
    sample_ratio: 1.0
    service_name: go-llm-agent
    metrics_interval_seconds: 30
```

### 5.3 公開インターフェース

```go
package obs

type TelemetryConfig struct {
    Enabled bool
    Endpoint string
    Insecure bool
    SampleRatio float64
    ServiceName string
    MetricsIntervalSeconds int
}

func InitTelemetry(ctx context.Context, c TelemetryConfig, logger *slog.Logger) (Shutdown, error)

type Shutdown func(context.Context) error

func StartAgentSpan(ctx context.Context, model string) (context.Context, trace.Span)
func StartLLMSpan(ctx context.Context, providerName, model string) (context.Context, trace.Span)
func StartToolSpan(ctx context.Context, toolName, callID string) (context.Context, trace.Span)

func RecordTokens(ctx context.Context, providerName, model string, in, out int)
func RecordToolOutcome(ctx context.Context, toolName string, ok bool, latency time.Duration)
func RecordRetry(ctx context.Context, providerName string, attempt int)
func RecordFallback(ctx context.Context, fromProvider, toProvider string)
```

`RecordRetry` と `RecordFallback` は 03 番設計書の retry Decorator から呼ばれます。01 番実装時にメトリクスインストゥルメントだけ用意しておき、実呼び出しは 03 番のタスクで配線します。

### 5.4 データフロー

1. main は `obs.InitTelemetry` を呼び shutdown を defer します。
2. agent.Run 開始時に AgentSpan を開始し、ループ内で LLMSpan と ToolSpan を子としてぶら下げます。
3. provider 実装は HTTP リクエスト前に LLMSpan を開始し、Usage を Recv した時に RecordTokens を呼びます。
4. tool.Registry の Decorator が Execute の前後で ToolSpan と RecordToolOutcome を呼びます。
5. slog のハンドラを TraceContextHandler でラップし、ログレコードに trace_id を載せます。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業内容 | 成果物 |
| --- | --- | --- | --- |
| 1.1 | RED | `internal/obs/otel_test.go` で `InitTelemetry` 無効時に no-op shutdown を返すテストを書く | テスト 1 件 |
| 1.2 | GREEN | `otel.go` を実装し、OTLP HTTP exporter と SDK 初期化を書く | 新ファイル |
| 1.3 | RED | `StartAgentSpan` が trace_id を context に伝播するテストを書く | テスト 1 件 |
| 1.4 | GREEN | 3 種類の Start ヘルパを実装 | 既ファイル拡張 |
| 1.5 | RED | provider に span が乗ることを mock provider で検証 | テスト 1 件 |
| 1.6 | GREEN | `internal/llm/openai/client.go` 他 4 provider の Chat と Stream を span でラップ | 各 provider 修正 |
| 1.7 | RED | tool.Registry が ToolSpan を発行するテスト | テスト 1 件 |
| 1.8 | GREEN | `internal/tool/registry.go` に Decorator を実装 | 既ファイル拡張 |
| 1.9 | RED | RecordTokens でメトリクスが増えるテスト | テスト 1 件 |
| 1.10 | GREEN | Meter とカウンタとヒストグラムを実装 | 既ファイル拡張 |
| 1.11 | REFACTOR | TraceContextHandler を slog に組み込み、tests/e2e/01-otel-trace.sh を追加 | E2E |

## 7. テスト計画

### 7.1 ユニット

- 無効時に no-op、有効時に exporter 初期化が成功すること。
- 各 Start ヘルパが span を context に積むこと。
- Meter のカウンタが増えること。

### 7.2 統合

- agent.Run のテスト内で fake exporter を仕込み、AgentSpan、LLMSpan、ToolSpan が親子で 1 トレースに乗ることを検証。
- カウンタとヒストグラムの値が期待値であること。

### 7.3 E2E

`tests/e2e/01-otel-trace.sh` を作成します。次を行います。

1. 一時 OTLP コレクタを docker は使わず `go run` で 4318 ポートに立てる軽量モック。
2. `./bin/agent run -p "echo test" --config tests/e2e/fixtures/otel-config.yaml` を実行。
3. モックが受信した span を JSON ダンプし、AgentSpan と LLMSpan と ToolSpan の親子関係を assert。

## 8. ロールアウト

`observability.otel.enabled` は既定 false です。有効化しても他機能の挙動は変わりません。設定 YAML の差分は `config.yaml.example` に追記します。

## 9. リスクと対策

- 依存追加でビルドサイズが増えます。`go.mod` の indirect を整理し、govulncheck を CI に維持します。
- 既存テストへの影響は Decorator の透過性で最小化します。

## 10. 完了基準

- `go test ./internal/obs/... -cover` が 80 パーセント以上です。
- `tests/e2e/01-otel-trace.sh` がローカルで成功します。
- README に Observability 節を追加し OTLP 接続例を載せます。
- `scripts/quality-gate.sh` に該当パッケージのテストが含まれます。
