# 03. LLM 呼び出しのリトライとバックオフとタイムアウト

## 1. 概要

LLM プロバイダー呼び出しに統一されたリトライ、指数バックオフ、ジッタ、リクエストタイムアウト、プロバイダーフォールバックを導入します。

## 2. 書籍根拠

Ch10 表 10-1 が retry logic と fallback frequency をメトリクス例として挙げ、Ch11 では信頼性のための feedback loop が前提とされています。Ch12 でも external threats への対応として throttling と graceful degradation を推奨しています。

## 3. 現状分析

`internal/llm/error.go` に `ProviderError` の `Retryable bool` フィールドはありますが、`internal/llm/openai/client.go` 他 4 provider の Chat と Stream は再試行ループを持ちません。HTTP クライアントのタイムアウトもデフォルトに依存しています。`config.yaml` から制御できる仕組みもありません。

## 4. ゴール

- 5xx と 429 と context タイムアウトのうち retryable と判断されたものは指数バックオフで自動再試行されます。
- 個別 provider にリクエストタイムアウトを設定できます。
- 一次プロバイダーが連続失敗したとき副プロバイダーへフォールバックします。
- リトライ回数とフォールバック発火回数はメトリクスとして観測できます。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/llm/retry` パッケージを新設し、Provider を Decorator で包みます。Decorator は ChatStream にも対応し、ストリーム読み取り中に retryable エラーが先頭で起きた場合のみ再試行します。フォールバックは Registry レイヤで実装します。

### 5.2 設定スキーマ

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

### 5.3 公開インターフェース

```go
package retry

type Config struct {
    MaxAttempts int
    InitialBackoff time.Duration
    MaxBackoff time.Duration
    JitterRatio float64
}

type Wrapped struct{ ... }

func WrapProvider(name string, p llm.Provider, c Config) llm.Provider
```

`llm.Registry` に `ResolveWithFallback(model string)` を追加し、fallback プロバイダーが設定されている場合は二段階で provider を返します。

### 5.4 データフロー

1. provider.Chat は Decorator 内で先頭リクエストを発行します。
2. ProviderError.Retryable が true のとき Decorator は指数バックオフ＋ジッタで待ちます。
3. MaxAttempts を超えるとエラーを返します。
4. agent.Service.Run はエラーが `ErrAllAttemptsFailed` のとき fallback provider を Registry に問い合わせ再実行します。
5. retry 回数と fallback 回数は OTel メトリクスにカウントします。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 3.1 | RED | retry のジッタとバックオフ計算の単体テスト |
| 3.2 | GREEN | `internal/llm/retry/retry.go` を実装 |
| 3.3 | RED | fake provider で 429 を 2 回返した後成功する統合テスト |
| 3.4 | GREEN | Decorator の Stream 対応を追加 |
| 3.5 | RED | Registry の ResolveWithFallback テスト |
| 3.6 | GREEN | `internal/llm/registry.go` を拡張 |
| 3.7 | RED | request_timeout_seconds が context.WithTimeout を仕込むテスト |
| 3.8 | GREEN | provider の HTTP client にタイムアウトを反映 |
| 3.9 | REFACTOR | metrics 連携と E2E を作成 |

## 7. テスト計画

### 7.1 ユニット

- バックオフが MaxBackoff を超えないこと。
- JitterRatio 0 で完全に決定論的に増えること。
- MaxAttempts を超えると `ErrAllAttemptsFailed` が返ること。

### 7.2 統合

- httptest で 429 と 200 を順に返すサーバを建て、openai provider が成功すること。
- fallback_to を設定し、一次が常時 500 のとき副が呼ばれて成功すること。

### 7.3 E2E

`tests/e2e/03-llm-retry.sh` で httptest を Go で起動し、`./bin/agent run` の挙動を観察します。リトライ回数を `/metrics` 風にエンドポイント経由で取得できる場合はそれも比較します。

## 8. ロールアウト

retry 設定を省略した場合は `MaxAttempts=1` 相当として現状の挙動を維持します。`request_timeout_seconds` 未指定なら 60 秒を既定とします。

## 9. リスクと対策

- 副作用のある tool 呼び出しの直後にリトライが走るとデータが壊れる可能性があります。retry は LLM 呼び出しのみに限定し、tool 実行はリトライ対象外と明記します。
- フォールバックがコスト差の大きな provider へ流れた場合、課金が想定外に膨らみます。fallback 発火時に warn ログを出します。

## 10. 完了基準

- `go test ./internal/llm/retry/... -cover` が 80 パーセント以上です。
- 既存 provider テストがすべて通り、リトライ統合テストが通ります。
- README にリトライ設定の説明を追記します。
