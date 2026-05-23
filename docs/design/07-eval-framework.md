# 07. 評価フレームワーク CLI

## 1. 概要

YAML 形式のゴールデンデータセットを定義し、`agent eval` サブコマンドで tool_recall、tool_precision、param_accuracy、phrase_recall を一括計測できる仕組みを実装します。本設計書では Hallucination 検知率は対象スコープ外とし、phrase_recall の派生指標として後続フェーズで追加します。

## 2. 書籍根拠

Ch9 「Integrating Evaluation into the Development Lifecycle」「Evaluating Tools」「Evaluating Planning」「Holistic Evaluation」を参照します。書籍のサンプルコードと metrics 定義を Go に書き直します。

## 3. 現状分析

`cmd/agent/main.go` にはサブコマンド `chat`、`run`、`serve`、`tools`、`config`、`version` のみがあります。回帰テスト用の evaluation セット枠組み、metrics 計算、LLM-as-judge 機構は存在しません。

## 4. ゴール

- `eval/cases/*.yaml` を読み込み、各ケースを agent.Run で実行し metrics を計算します。
- `agent eval --suite <dir> --report <file>` で JSON レポートを出力します。
- レポートの aggregated metrics を terminal に表示します。
- 回帰検出のため、metrics 閾値を CI クオリティゲートで比較できます。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/eval` パッケージを新設し、Loader、Runner、Scorer、Reporter を分離します。Loader は YAML を構造体に読み込み、Runner は fake provider もしくは実 provider で agent.Run を呼びます。Scorer は metrics を計算し、Reporter は JSON とテキストの 2 種類を出力します。

### 5.2 ファイル形式

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
        item: mug
  phrases:
    - "返金処理を受け付けました"
metrics:
  tool_recall_min: 1.0
  param_accuracy_min: 1.0
  phrase_recall_min: 0.5
```

### 5.3 公開インターフェース

```go
package eval

type Case struct {...}

// RunResult eval Suite または 16 番 shadow 実行のいずれからも生成可能な共通 JSON 形式
// `--input-type shadow` フラグで shadow recorder の出力ファイルを Case 推定なしで読み込める
type RunResult struct {
    CaseID string
    Source string  // "suite" または "shadow"
    ToolCalls []llm.ToolCall
    FinalText string
}

type Scorer interface {
    Score(c Case, r RunResult) Scores
}

type Scores struct {
    ToolRecall float64
    ToolPrecision float64
    ParamAccuracy float64
    PhraseRecall float64
    Passed bool
}

func LoadSuite(dir string) ([]Case, error)
func RunSuite(ctx context.Context, svc agent.Service, cases []Case) ([]RunResult, error)
func WriteReport(path string, cases []Case, results []RunResult, scores []Scores) error
```

### 5.4 データフロー

1. `agent eval` は config と registry を準備し、suite を読み込みます。
2. Runner は各 Case の messages を agent.Run に流します。
3. EventToolCall を集めて Scorer に渡します。
4. Reporter が JSON ファイルを書き出し、aggregated 値を stdout に表示します。
5. いずれかの metrics 閾値を下回ると exit code を 1 にします。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 7.1 | RED | LoadSuite と Case の YAML パーステスト |
| 7.2 | GREEN | loader.go を実装 |
| 7.3 | RED | Scorer の metrics 計算テスト |
| 7.4 | GREEN | scorer.go を実装 |
| 7.5 | RED | RunSuite を fake provider で実行する統合テスト |
| 7.6 | GREEN | runner.go を実装 |
| 7.7 | RED | Reporter JSON 出力テスト |
| 7.8 | GREEN | reporter.go を実装 |
| 7.9 | REFACTOR | `cmd/agent/main.go` に eval サブコマンドを追加、E2E 作成 |

## 7. テスト計画

### 7.1 ユニット

- 期待 tool が 0 件のとき recall=1.0 で扱うこと。
- params が一致しない場合の param_accuracy 計算。
- phrase_recall が部分一致で正しく計上されること。

### 7.2 統合

- 3 ケースを含む fixture を読み、aggregated metrics の正確さを確認。

### 7.3 E2E

`tests/e2e/07-eval-suite.sh` で `eval/cases/` 配下に 2 ケースの YAML を投入し、`./bin/agent eval --suite eval/cases --report /tmp/report.json` を実行、JSON の構造と exit code を確認します。

## 8. ロールアウト

実 provider を呼ぶ場合は遅延とコストがかかるため、`--provider` 引数で fake provider を指定可能にします。CI では fake provider 必須とします。

## 9. リスクと対策

- LLM 出力の揺らぎで brittle なテストになる懸念があります。Scorer には正規表現での `phrase_recall` を提供して柔軟性を確保します。
- 機密データを suite に含めないよう `eval/cases/` を `.gitleaks.toml` の対象に追加します。

## 10. 完了基準

- `go test ./internal/eval/... -cover` が 80 パーセント以上です。
- `tests/e2e/07-eval-suite.sh` が成功します。
- README と docs/usage.md に `agent eval` を追記します。
