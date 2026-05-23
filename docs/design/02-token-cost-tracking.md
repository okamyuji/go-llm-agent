# 02. トークン使用量とコスト集計

## 1. 概要

LLM プロバイダーが返す Usage 情報を agent.Event に伝播させ、セッション単位および日次の集計、料金換算、上限到達時の停止機構を実装します。

## 2. 書籍根拠

Ch10 表 10-1 では Workflow level のメトリクスとして Token usage を挙げ、急増が異常検知の入口になると述べています。Ch11 では prompt と tool の改善ループにおいて per-session のコストを観察することが推奨されます。

## 3. 現状分析

`internal/llm/message.go` には `Usage{InputTokens, OutputTokens}` 型が既にあります。しかし `internal/agent/loop.go` の Event ストリームには Usage を含めるフィールドがなく、各 provider の Stream 実装でも `ev.Usage` を読み飛ばしています。session store には Entry しか保存されず、token と料金の集計手段はありません。

## 4. ゴール

- agent.Event に Usage が乗り、session 書き込み時に hop 単位で残ります。
- セッション総トークンと推定コストが計算できます。
- 設定された予算上限を超えたとき agent.Run はエラーで停止します。
- `/v1/usage` エンドポイントで集計を返します。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/billing` パッケージを新設し、料金表とアキュムレータを保持します。`agent.Service` はループ内で provider が返す Usage を `billing.Accumulator` に渡し、所定の閾値で `ErrBudgetExceeded` を返します。

### 5.2 設定スキーマ

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

### 5.3 公開インターフェース

```go
package billing

type Pricing struct {
    InputPerMillionJPY float64
    OutputPerMillionJPY float64
}

type Snapshot struct {
    SessionID string
    Provider string
    Model string
    InputTokens int
    OutputTokens int
    CostJPY float64
}

type Accumulator interface {
    Add(ctx context.Context, providerName, model string, in, out int) (Snapshot, error)
    SessionTotal(sessionID string) Snapshot
    DailyTotal(date string) Snapshot
}

type Store interface {
    Append(ctx context.Context, s Snapshot) error
    QuerySession(ctx context.Context, sessionID string) ([]Snapshot, error)
    QueryDate(ctx context.Context, date string) ([]Snapshot, error)
}

var ErrBudgetExceeded = errors.New("budget exceeded")
```

`internal/agent/agent.go` の `Event` に `Usage *llm.Usage` と `Cost *billing.Snapshot` を追加します。

### 5.4 データフロー

1. provider.Stream の最終 StreamEvent が `Usage` を持ちます。loop.go はそれを取り出し `Accumulator.Add` に渡します。
2. Accumulator は単価表を引いて Snapshot を作り、Store に append します。
3. 戻ってきた累計が予算上限を超えるなら `ErrBudgetExceeded` を返し、loop はエラーイベントを emit して終了します。
4. transport/httpapi に `/v1/usage?session=<id>` および `/v1/usage?date=YYYY-MM-DD` を新設します。
5. session store と billing store は同じ basedir に分離して JSONL で永続化します。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 | 成果物 |
| --- | --- | --- | --- |
| 2.1 | RED | `internal/billing/accumulator_test.go` で単価計算と通貨換算の単体テスト | テスト |
| 2.2 | GREEN | `accumulator.go` を実装 | 新ファイル |
| 2.3 | RED | Store のファイル永続化テスト | テスト |
| 2.4 | GREEN | `store.go` を JSONL で実装 | 新ファイル |
| 2.5 | RED | `internal/agent/loop_budget_test.go` で予算超過の挙動 | テスト |
| 2.6 | GREEN | loop.go に Usage 取り込みと閾値判定を実装 | 既ファイル拡張 |
| 2.7 | RED | `/v1/usage` の HTTP テスト | テスト |
| 2.8 | GREEN | transport/httpapi/usage.go を実装 | 新ファイル |
| 2.9 | REFACTOR | config.yaml.example に pricing と budget を追記、E2E を作成 | ドキュメント |

## 7. テスト計画

### 7.1 ユニット

- 単価ゼロのとき CostJPY も 0 であること。
- 予算超過直前まで通過し、超過 hop で `ErrBudgetExceeded` が返ること。

### 7.2 統合

- fake provider が返す Usage が Event ストリームと Store に同時に反映されること。
- session を 2 回 Run しても DailyTotal が累積すること。

### 7.3 E2E

`tests/e2e/02-token-budget.sh` を作成し、`agent run` を 2 回呼び 2 回目で予算超過を意図的に発生させて exit code が非ゼロになることを検証します。さらに `curl http://127.0.0.1:14000/v1/usage?date=...` の JSON を assert します。

## 8. ロールアウト

`agent.budget.session_max_tokens` と `daily_max_cost_jpy` を未設定だと無制限です。pricing 未指定の provider は Cost を 0 として扱います。

## 9. リスクと対策

- 高頻度書き込みで JSONL がボトルネックになる懸念があります。バッファリングと fsync 頻度を設定できるようにします。
- 為替変動を無視しているため JPY 固定です。将来の通貨拡張に備え `Currency string` を Pricing に保持しておきます。
- 通貨表現は MVP で float64 を採用しているため、極端な累積でわずかな丸め誤差が予算境界をすり抜ける理論的リスクがあります。本番運用で 1 円未満の精度が要求された場合は、`int64` の最小通貨単位 (銭) または `shopspring/decimal` 等の固定小数点へ移行する想定です (Heavy lift のため別フェーズで扱います)。

## 10. 完了基準

- `go test ./internal/billing/... -cover` が 80 パーセント以上です。
- `tests/e2e/02-token-budget.sh` が成功します。
- `/v1/usage` の OpenAPI 互換でない独自レスポンスを README に記載します。
- 既存の `/v1/chat/completions` のレスポンスにも `usage` フィールドが正しく返ります。
