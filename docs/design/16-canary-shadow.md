# 16. カナリアとシャドウデプロイ

## 1. 概要

新しいモデルやプロンプトテンプレートを段階的に本番に導入できるよう、リクエストの一定割合を別モデルにルーティング（canary）、もしくはバックグラウンドで並列実行して結果を比較記録する（shadow）機構を実装します。

## 2. 書籍根拠

Ch11「Shadow Deployments」「Canary Deployments」を参照します。書籍では blast radius の制限と差分計測の重要性を述べています。

## 3. 現状分析

`default_model` は単一指定のみで、リクエスト単位のサンプリング、シャドウ実行、差分記録はありません。

## 4. ゴール

- canary ratio で指定割合のリクエストを別 model に振り分けられます。
- shadow ratio で別 model にバックグラウンド実行させて結果を記録します（ユーザーには返しません）。
- すべてのルーティング情報は OTel span 属性および session メタに残ります。
- canary または shadow の失敗で本流が阻害されません。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/agent/routing.go` を新設し、Service の前段で Router が動きます。Router は乱数で実行戦略を決定し、shadow の場合は goroutine で別 Run を実行し、結果は 07 番の eval パイプラインに似た形式で保存します。

### 5.2 設定スキーマ

```yaml
agent:
  canary:
    enabled: false
    model: anthropic/claude-3-5-sonnet-latest
    ratio: 0.05
  shadow:
    enabled: false
    model: openai/gpt-4o
    ratio: 0.10
    record_dir: ~/.local/state/go-llm-agent/shadow  # 環境変数の展開は未実装のため明示パスを指定する
```

### 5.3 公開インターフェース

```go
type Router struct{...}

func NewRouter(cfg AgentConfig) *Router

func (r *Router) Pick(seed int64) Decision

type Decision struct {
    Primary string
    Shadow string
    UseCanary bool
}
```

### 5.4 データフロー

1. agent.Service.Run 開始時に Router.Pick が呼ばれます。
2. UseCanary が true なら Primary を canary.model に差し替えます。canary 経路では system プロンプトテンプレート（13 番）も明示的に切り替えない限り primary と同じものを引き継ぎます。テンプレートを別バージョンで比較したい場合は `agent.canary.system_prompt_template` を別途指定できる拡張点を 13 番の Loader 経由で受け付けます。
3. Shadow が非空なら別 goroutine で同じ Input を別 model で実行し、結果を `record_dir/<runID>.json` に書き出します。
4. ユーザーに返るのは Primary 側の結果のみです。
5. shadow recorder が出力する JSON は 07 番設計書の `eval.RunResult` 型に準拠し、`source="shadow"` を埋めます。07 番の eval CLI は `agent eval --input-type shadow --dir <record_dir>` で読み込めるよう拡張します。これによりカナリア/シャドウとオフライン回帰評価の指標が同じ Scorer で算出されます。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 16.1 | RED | Router の確率分布テスト（ratio 0.0 と 1.0） |
| 16.2 | GREEN | routing.go を実装 |
| 16.3 | RED | shadow goroutine が primary の終了より前に cancel される挙動テスト |
| 16.4 | GREEN | shadow runner を実装 |
| 16.5 | RED | shadow record の JSON 構造テスト |
| 16.6 | GREEN | recorder を実装 |
| 16.7 | REFACTOR | E2E `tests/e2e/16-canary-shadow.sh` |

## 7. テスト計画

### 7.1 ユニット

- `Pick(seed)` は決定論的なので、ratio=0.0 では UseCanary=false が必ず返ること、ratio=1.0 では UseCanary=true が必ず返ることを境界値テストで確認します。中間 ratio の統計テストはシードに依存して flaky になるため採用しません。
- shadow 結果が primary を阻害しないこと（fake provider で sleep を仕込んで観測）。

### 7.2 統合

- agent.Service.Run の挙動が canary 有無で外見的に変わらず、span 属性のみが変わること。

### 7.3 E2E

`tests/e2e/16-canary-shadow.sh` で fake provider 2 つを立て、canary.ratio=1.0 で 100 パーセント canary に振られることを確認します。

## 8. ロールアウト

既定 OFF で運用し、徐々に ratio を上げる前提です。canary のメトリクスは OTel に流し Grafana で比較します。

## 9. リスクと対策

- shadow 実行がコスト膨張に直結するため、`shadow.ratio` の上限を 0.5 にハードコードします。
- shadow が外部副作用を起こさないよう、副作用ツール（fs_write、shell）は shadow 経路では disable します。

## 10. 完了基準

- agent/routing パッケージのカバレッジ 80 パーセント。
- E2E 成功。
- README に Canary と Shadow の節を追加します。
