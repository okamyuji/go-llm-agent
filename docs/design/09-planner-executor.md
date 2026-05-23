# 09. Planner-Executor および Reflection 戦略

## 1. 概要

単純な ReAct ループに加えて、計画と実行を別モデルに分離する Planner-Executor 戦略、出力の自己批評と再試行を行う Reflection 戦略を実装し、設定で切り替えられるようにします。

## 2. 書籍根拠

Ch5「Planner-Executor Agents」「Reflection Agents」を参照します。書籍はそれぞれの戦略について計算コストと観測しやすさのトレードオフを論じています。Query-Decomposition は本書では Planner-Executor の一形態とみなし、planner_model のシステムプロンプトに「複雑な質問は副問いに分解してから計画を出力する」旨を含めることで吸収します。Query-Decomposition 専用の戦略は実装しません。

## 3. 現状分析

`internal/agent/loop.go` は単一の ReAct ライクなループしか提供しません。戦略の差し替えポイントは存在しません。

## 4. ゴール

- `agent.strategy` 設定で `react`、`planner_executor`、`reflection` を選択できます。
- Planner と Executor を別モデルに割り当てられます。
- Reflection は失敗閾値到達時に自己批評ターンを差し込みます。
- 各戦略は同一の Event ストリームインターフェースを保ちます。

## 5. 設計

### 5.1 アーキテクチャ概要

MVP 実装では `internal/agent/strategy.go` (package agent) を 1 ファイルにまとめ、同一パッケージ内で `Strategy` インターフェースと 3 戦略 (reactStrategy / plannerExecutorStrategy / reflectionStrategy) を定義します。`Strategy.run` は非公開メソッドとして `*service` を受け取り、loop.go の `runReAct` に委譲します。Service は Strategy を受け取り Run に委譲します。サブパッケージ (`internal/agent/strategy`) への分離は本実装の成熟後に検討します。

### 5.2 設定スキーマ

```yaml
agent:
  strategy: react
  planner_executor:
    planner_model: openai/gpt-4o
    executor_model: openai/gpt-4o-mini
    max_steps: 8
  reflection:
    max_iterations: 3
    trigger:
      consecutive_failures: 2
      hop_budget: 6
```

### 5.3 公開インターフェース

```go
package strategy

type Strategy interface {
    Run(ctx context.Context, in agent.Input, out chan<- agent.Event) error
}

type ReAct struct{...}
type PlannerExecutor struct{...}
type Reflection struct{...}

func New(cfg config.AgentConfig, reg llm.Registry, tools tool.Registry) (Strategy, error)
```

### 5.4 データフロー

1. agent.New は config から Strategy を構築します。
2. ReAct は現状のループをそのまま使います。
3. PlannerExecutor はまず planner_model に「計画を JSON 配列で出せ」を依頼し、各 step を executor_model で実行します。executor はツールのみ呼べる system 制約を持ちます。
4. Reflection は各 hop の最後に短い `self_check` プロンプトを LLM に投げ、矛盾検出時に修正試行を 1 ターン追加します。失敗閾値到達時のみ作動します。
5. すべての戦略で Event の Kind とフォーマットは共通です。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 9.1 | RED | Strategy インターフェースとファクトリの単体テスト |
| 9.2 | GREEN | strategy パッケージを骨組みごと実装 |
| 9.3 | RED | `internal/agent/loop_test.go` の ReAct シナリオを `internal/agent/strategy_test.go` 側でカバーする (MVP では同一パッケージのため import は変更不要)。サブパッケージ化を行う将来フェーズで初めてファイル移送を検討する |
| 9.4 | GREEN | strategy.go の reactStrategy を実装 |
| 9.5 | RED | PlannerExecutor の計画 JSON パーステスト |
| 9.6 | GREEN | planner_executor.go を実装 |
| 9.7 | RED | Reflection の self_check トリガーテスト |
| 9.8 | GREEN | reflection.go を実装 |
| 9.9 | REFACTOR | E2E `tests/e2e/09-planner-executor.sh` を作成 |

## 7. テスト計画

### 7.1 ユニット

- Planner が壊れた JSON を返したらフォールバックすること。
- Reflection が trigger 条件を満たすときだけ作動すること。

### 7.2 統合

- httptest fake provider を 2 つ立て、planner と executor の役割が別 URL で呼ばれていることを assert。

### 7.3 E2E

`tests/e2e/09-planner-executor.sh` で fake provider を立ち上げ、planner と executor で別 model 名がリクエストされることを確認します。

## 8. ロールアウト

`agent.strategy` の既定値は `react` で互換性を保ちます。Planner-Executor を有効化するときは planner_model と executor_model の指定が必須です。

## 9. リスクと対策

- Planner 出力の JSON 不安定性に備え、フォーマット強制プロンプトを 2 回再試行します。
- Reflection の追加コールでコストが膨らむため、`trigger.consecutive_failures` 条件で必要時のみ作動させます。

## 10. 完了基準

- strategy パッケージのカバレッジ 80 パーセント。
- 3 戦略すべてが E2E でルートトレースに乗ること。
- README に戦略選択の節を追加します。
