# 13. プロンプトテンプレート版管理

## 1. 概要

system プロンプトを単一の YAML 文字列から脱却させ、ファイルベースのテンプレートとして版管理し、安全な変数差し込みのみ許可する仕組みを導入します。

## 2. 書籍根拠

Ch10 表 10-1 では Task success rate by workflow or prompt template version を観測指標として推奨しています。Ch11「Prompt and Tool Refinement」では版を切って A/B 比較する重要性が強調されます。

## 3. 現状分析

`config.AgentConfig.SystemPrompt` は単一の YAML 文字列であり、版や差し替えの履歴を残せません。テンプレート変数の置換ロジックもありません。

## 4. ゴール

- `prompts/<name>@<version>.tmpl` でテンプレートを管理できます。
- agent.system_prompt_template に `name@version` 形式で参照できます。
- テンプレート変数は `.user_id`、`.now`、`.tools_summary` のみホワイトリスト化します。
- OTel span に `prompt.version` 属性を載せます。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/prompt` パッケージを新設し、`Loader` と `Renderer` を提供します。Renderer は `text/template` のうち関数呼び出しをホワイトリストに限定します。

### 5.2 設定スキーマ

```yaml
agent:
  system_prompt_template: refund@v3
prompts:
  dir: ./prompts
  variables:
    - now
    - tools_summary
```

### 5.3 公開インターフェース

```go
package prompt

type Template struct { Name string; Version string; Body string }

type Loader interface {
    Load(ref string) (Template, error)
}

type Renderer interface {
    Render(t Template, vars map[string]any) (string, error)
}
```

### 5.4 データフロー

1. main.cmdRun と cmdChat は Loader で Template を取得します。
2. Renderer は許可された変数だけを map に詰めて `text/template` に渡します。
3. 結果を agent.Input.SystemPrompt にセットし、OTel span 属性に `prompt.version=v3` を追加します。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 13.1 | RED | Loader のファイル探索テスト |
| 13.2 | GREEN | loader.go を実装 |
| 13.3 | RED | Renderer の変数ホワイトリスト違反テスト |
| 13.4 | GREEN | renderer.go を実装 |
| 13.5 | RED | agent.Input への注入テスト |
| 13.6 | GREEN | main.cmdRun と cmdChat を改修 |
| 13.7 | REFACTOR | E2E `tests/e2e/13-prompt-template.sh` |

## 7. テスト計画

### 7.1 ユニット

- 同名複数バージョンが正しく取れること。
- 未許可関数（例: `os.Getenv`）を呼ぼうとするとエラーになること。

### 7.2 統合

- chat REPL でテンプレートを差し替えて起動できること。

### 7.3 E2E

`tests/e2e/13-prompt-template.sh` で `prompts/sample@v1.tmpl` をリポジトリに置き、`agent run` で version 属性が span に乗ったか fake exporter 経由で確認します。

## 8. ロールアウト

`system_prompt_template` が空のときは現状互換で `config.AgentConfig.SystemPrompt` をそのまま用います。両方指定された場合は template が優先します。

## 9. リスクと対策

- テンプレート展開で `{{ .secret }}` のような誤用を防ぐため、未許可キーは render エラーにします。
- 大量のテンプレートで起動が遅延しないよう、lazy load を採用します。

## 10. 完了基準

- prompt パッケージのカバレッジ 80 パーセント。
- E2E 成功。
- docs に prompts ディレクトリ命名規約を追加します。
