# 10. 並列ツール実行

## 1. 概要

1 ホップで複数のツールを並列に呼べるよう、agent ループとプロバイダー実装、Event ストリームを拡張します。

## 2. 書籍根拠

Ch5「Parallel Tool Execution」を参照します。OpenAI、Anthropic、Gemini ともに parallel tool calls をネイティブにサポートしているため、これを活用すると応答時間が大幅に縮みます。

## 3. 現状分析

`internal/llm/message.go` の `Message.ToolCalls []ToolCall` は配列ですが、`internal/agent/loop.go` は `var pendingCall *llm.ToolCall` と単数で扱い、StreamEvent も最後の 1 件しか取りません。

## 4. ゴール

- 1 ホップに複数の ToolCall を並列実行できます。
- 結果順序は ToolCall の ID で決定論的に揃います。
- 失敗時は他の並列実行を待つかキャンセルするか設定可能です。
- 並列度の上限を設定できます。

## 5. 設計

### 5.1 アーキテクチャ概要

provider 側で `StreamEvent.ToolCalls []ToolCall` を返せるよう拡張し、agent loop で `errgroup` を使った並列実行を実装します。

### 5.2 設定スキーマ

```yaml
agent:
  parallel_tools:
    enabled: true
    max_concurrency: 4
    fail_fast: false
```

### 5.3 公開インターフェース

```go
type StreamEvent struct {
    DeltaText string
    ToolCalls []ToolCall
    Usage *Usage
    Finish string
    Err error
}
```

agent.Event に `ToolResults []ToolResult` を追加し、`EventToolResult` の単数版と並存させます。

### 5.4 データフロー

1. provider Stream は ToolCalls をスライスでまとめて emit します。
2. loop.go は ToolCalls を 2 段階で処理します。最初に `require_approval=true` のツールが 1 件でも含まれるかを判定します。
3. 含まれる場合はバリア方式に切り替え、`require_approval=true` のツールを 1 件ずつ順番に approve して実行し、approve 待ち中は他の `require_approval=false` のツールも errgroup に投入しません。これは並列実行の最中に HITL プロンプトが出現すると人間オペレータが状況把握できなくなるためです。
4. すべて `require_approval=false` の場合のみ errgroup で並列実行し、`ctx context.WithCancel` で fail_fast 時の連鎖キャンセルを行います。
5. `fail_fast=true` の場合は最初の失敗で他の goroutine をキャンセルします。
6. 全結果を ID 順に並べて Message に戻します。
7. OTel span は各 ToolCall ごとに分けて記録します。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 10.1 | RED | StreamEvent の複数 ToolCalls テスト |
| 10.2 | GREEN | message.go と provider 実装を更新 |
| 10.3 | RED | loop.go の errgroup 並列テスト |
| 10.4 | GREEN | loop を改修 |
| 10.5 | RED | max_concurrency 上限テスト |
| 10.6 | GREEN | semaphore を追加 |
| 10.7 | REFACTOR | E2E `tests/e2e/10-parallel-tools.sh` |

## 7. テスト計画

### 7.1 ユニット

- 3 件の ToolCall が同時に実行されること（sleep を仕込んで観測）。
- 失敗 1 件があっても fail_fast=false なら他が完了すること。

### 7.2 統合

- fake provider が 2 個の ToolCall を一度に返すシナリオで結果が両方反映されること。

### 7.3 E2E

`tests/e2e/10-parallel-tools.sh` で local provider と 2 本の `search_files` 並列呼び出しを行い、合計レイテンシが直列より短いことを assert します。

## 8. ロールアウト

`parallel_tools.enabled` 既定 false で互換性を維持し、明示的に有効化したときのみ並列実行を行います。

## 9. リスクと対策

- 副作用ツール（fs_write、shell）の同時実行は危険なため、`require_approval` 付きツールは強制的に直列化します。
- goroutine 漏れを防ぐため、`go.uber.org/goleak` の TestMain チェックを継続します。

## 10. 完了基準

- agent パッケージのカバレッジを 80 パーセント以上に維持します。
- 並列 E2E が成功します。
- README に並列ツール実行の説明を追加します。
