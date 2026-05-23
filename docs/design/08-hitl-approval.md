# 08. Human-in-the-Loop ツール実行承認

## 1. 概要

リスクのあるツールに対して人間の事前承認を要求する仕組みを CLI と HTTP API の双方に導入し、エスカレーションフローを記録できるようにします。

## 2. 書籍根拠

Ch11「Human-in-the-Loop Review」と Ch13「Escalation Design and Oversight」を参照します。書籍は信頼度、リスク、影響範囲のいずれかで人間に介入させる閾値を設けるべきと述べています。

## 3. 現状分析

`internal/tool/registry.go` は許可リストでツールを有効化するだけで、実行前承認の仕組みはありません。`fs_write` や `shell` も sandbox を通過すれば即時実行されます。

## 4. ゴール

- ツール定義に `require_approval` フラグを設けます。
- 該当ツールを呼ぶ際、agent.Run は EventToolApprovalRequest を流し、承認応答を待ちます。
- CLI では対話プロンプト、HTTP では `/v1/runs/<id>/approve` で操作可能です。
- セッションには承認結果と理由が監査ログとして残ります。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/agent/approval.go` を新設し、ApprovalChannel と ApprovalDecision を定義します。loop.go は ToolCall を受け取ったとき、対象ツールの `RequireApproval` が真なら EventToolApprovalRequest を emit し、ApprovalChannel から `Decision` を待ちます。

### 5.2 設定スキーマ

```yaml
agent:
  approval:
    # 承認必須にしたいツール名を列挙する (ツール側に require_approval フラグを持たせない設計)
    required_tools:
      - shell
      - fs_write
    timeout_seconds: 30
    # default_decision は "deny" のみサポート (allow は fail-open のため廃止)
    default_decision: deny
```

旧版の設計案では `tools.fs.require_approval: write` のようにツール定義側にフラグを持たせる案がありましたが、設定の集約性とランタイム判定の簡潔さを優先し、`agent.approval.required_tools` リスト形式に統一しています。

### 5.3 公開インターフェース

```go
type ApprovalRequest struct {
    RunID string
    CallID string
    ToolName string
    Arguments json.RawMessage
}

type ApprovalDecision struct {
    RunID string
    CallID string
    Allowed bool
    Reason string
    Reviewer string
}

type Approver interface {
    Request(ctx context.Context, r ApprovalRequest) (ApprovalDecision, error)
}
```

agent.Input に `RunID string` を追加し、agent.Service.Run 開始時に UUID v4 で自動生成します（外部から指定された場合は尊重）。HTTP Approver は `sync.Map[runID -> chan ApprovalDecision]` を内部に保持し、`/v1/runs/<runID>/approve` 受信時に該当 channel に Decision を送ります。Run 完了時に必ず channel を close し、対応する map エントリを Delete します。これにより同時実行中の複数 run の HITL を独立して扱えます。

agent.Service は `WithApprover(Approver)` でラップ可能にします。CLI と HTTP それぞれの Approver 実装を用意します。

### 5.4 データフロー

1. loop.go が ToolCall を受信したら approver.Request を呼びます。
2. CLI Approver は REPL の標準入力で y/N と理由を聞き取ります。
3. HTTP Approver は internal channel に push し、`/v1/runs/<id>/approve` で外部から JSON を受け取ります。
4. Decision が denied なら ToolResult を `IsError=true`、Content にレビュアー理由を入れて LLM に戻します。
5. timeout を超えると default_decision に従い処理します。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 8.1 | RED | Approver インターフェースの単体テスト |
| 8.2 | GREEN | CLI Approver を実装 |
| 8.3 | RED | HTTP Approver の channel テスト |
| 8.4 | GREEN | HTTP Approver と /v1/runs/<id>/approve を実装 |
| 8.5 | RED | loop.go の EventToolApprovalRequest テスト |
| 8.6 | GREEN | loop.go を改修 |
| 8.7 | REFACTOR | 監査ログに Decision を載せ、E2E を作成 |

## 7. テスト計画

### 7.1 ユニット

- timeout の挙動。
- default_decision=deny のとき timeout 後にツール非実行であること。

### 7.2 統合

- HTTP API で `await` 中に `/approve` を叩いて、ストリームが続行すること。

### 7.3 E2E

`tests/e2e/08-hitl-approval.sh` で `shell` ツールを要求するプロンプトを実行し、deny で停止することを確認します。

## 8. ロールアウト

`require_approval` 未設定なら現状互換です。`agent.approval.timeout_seconds` の既定値は 30 秒、`default_decision` の既定値は deny です。

## 9. リスクと対策

- HTTP モードで run id を外部に漏らさないために `/v1/runs` リストエンドポイントは公開しません。
- ApprovalChannel のメモリリークを防ぐため、run 終了時に必ず close します。
- Slack や PagerDuty などの外部 Webhook 通知は本設計書のスコープ外です。将来フェーズで Approver の上位 Decorator として `WebhookNotifier` を追加する拡張点だけ準備します。

## 10. 完了基準

- approval ユニットテストカバレッジ 80 パーセント。
- E2E 成功。
- README に HITL の設定例を載せます。
