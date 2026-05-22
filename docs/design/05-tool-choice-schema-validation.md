# 05. tool_choice と JSON スキーマ強制

## 1. 概要

LLM プロバイダー API の `tool_choice` パラメータを統一インターフェースで露出し、ツール入力 JSON を `tool.Spec.Schema` に照らして検証、失敗時はモデルに対して修正プロンプトを返すループを実装します。

## 2. 書籍根拠

Ch4 「Tool Use Configuration」で `auto`、`required`、`none`、特定ツール指定の 4 モードを推奨しています。さらに「Validate first using jsonschema or Pydantic」「Retry intelligently」を出力検証の基本ガイドラインとして示しています。

## 3. 現状分析

`internal/llm/llm.go` の `ChatRequest` は `Tools []ToolSpec` を持ちますが `ToolChoice` フィールドはありません。各 provider の HTTP リクエスト構築でも tool_choice を渡していません。`agent/loop.go` は `pendingCall.Arguments` の JSON を `Tool.Execute` にそのまま渡し、各 tool 内で個別バリデーションを行っています。スキーマ違反時の修正プロンプトもありません。

## 4. ゴール

- agent.Input から tool_choice を指定できます。
- すべての provider が tool_choice を尊重します。
- tool.Spec.Schema に基づくバリデーションが agent loop の中央で行われます。
- バリデーション失敗時には ToolCall を破棄してエラー観測可能なメッセージを LLM に返し、最大 N 回まで修正を試みます。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/llm/llm.go` に `ToolChoice` 型を追加し、各 provider の Stream/Chat 実装で API 仕様に変換します。`internal/agent/validate.go` を新設し、JSON Schema 検証（github.com/xeipuuv/gojsonschema）を行います。

### 5.2 設定スキーマ

```yaml
agent:
  tool_choice:
    mode: auto
    name: ""
  tool_validation:
    enabled: true
    max_retries: 2
```

### 5.3 公開インターフェース

```go
type ToolChoice struct {
    Mode string
    Name string
}

type ChatRequest struct {
    Model string
    Messages []Message
    Tools []ToolSpec
    ToolChoice *ToolChoice
    Temperature *float64
    MaxTokens *int
}
```

`internal/agent/validate.go` は次を提供します。

```go
type SchemaValidator interface {
    Validate(toolName string, args json.RawMessage) (ok bool, msg string)
}

func NewSchemaValidator(reg tool.Registry) SchemaValidator
```

### 5.4 データフロー

1. agent.Run は ChatRequest 構築時に Input.ToolChoice を copy します。
2. 各 provider は `auto`、`required`、`none`、`tool:<name>` を API 固有の表現に変換します。
3. ToolCall を受け取った直後、loop.go は Validator を呼びます。
4. 失敗の場合は ToolResult を `IsError=true`、Content にバリデーションメッセージを入れて LLM に戻し、max_retries まで継続します。
5. 連続失敗が max_retries を超えると EventError で打ち切ります。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 5.1 | RED | ToolChoice をフィールドに含む ChatRequest テスト |
| 5.2 | GREEN | llm.go と provider 4 種類の HTTP request 変換 |
| 5.3 | RED | SchemaValidator の検証成功/失敗テスト |
| 5.4 | GREEN | gojsonschema で実装 |
| 5.5 | RED | loop.go がバリデーション失敗時に修正プロンプトを LLM に投げ直すテスト |
| 5.6 | GREEN | loop.go を改修 |
| 5.7 | REFACTOR | E2E `tests/e2e/05-tool-choice-validation.sh` を作成 |

## 7. テスト計画

### 7.1 ユニット

- 4 つの ToolChoice モードがそれぞれ正しい API JSON に変換されること。
- スキーマ違反のとき詳細メッセージが返ること。

### 7.2 統合

- httptest provider で `required` を指定したら必ず ToolCall が返るシナリオ。
- 壊れた JSON を返したときに 2 回まで修正試行が走ること。

### 7.3 E2E

ローカル `fs_read` ツールを壊れた引数で叩こうとする会話を 1 ターン回し、修正試行を経て成功するか、max_retries 超過で失敗するかを確認します。

## 8. ロールアウト

`agent.tool_validation.enabled` の既定値は true、`max_retries` の既定値は 2 とします。`ToolChoice` 未指定の挙動は現状互換の auto です。

## 9. リスクと対策

- 一部 provider が tool_choice を未サポートな場合は client 側で `none` 以外を無視し warn ログを出します。
- gojsonschema の追加で依存が増えるため govulncheck を CI に維持します。

## 10. 完了基準

- 全 provider の Stream/Chat テストが tool_choice 指定込みで成功。
- `tests/e2e/05-tool-choice-validation.sh` が成功。
- README に Tool Use Configuration の節を新設し設定例を載せます。
