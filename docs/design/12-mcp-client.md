# 12. MCP クライアントによる動的ツール発見

## 1. 概要

外部 MCP サーバへ JSON-RPC 接続し、`tools/list` で発見したメソッドを動的に tool.Registry に登録できる MCP クライアントを実装します。

## 2. 書籍根拠

Ch4「Model Context Protocol」を参照します。書籍は MCP がエージェントのツール抽象を標準化し、bespoke なアダプタを廃するための鍵だと述べています。

## 3. 現状分析

ツールは Go コードで静的に登録するのみです。外部プロセスを介して動的に発見する機構は存在しません。

## 4. ゴール

- stdio と SSE の 2 つの transport で MCP サーバへ接続できます。
- サーバが提供するツールを agent 起動時に発見して registry に追加します。
- `allow_methods` で受け入れるメソッドを絞り込めます。
- セキュリティのため sandbox と監査ログは既存ツールと同じ流儀で適用します。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/mcp` パッケージを新設し、`Client`、`Transport`、`Discovery` の 3 抽象を定義します。Discovery は起動時に `tools/list` を呼び、`tool.Tool` 実装である `MCPTool` を生成します。

### 5.2 設定スキーマ

```yaml
tools:
  mcp_servers:
    - name: docs
      transport: stdio
      command: ["./mcp/docs_server"]
      allow_methods: ["search_docs", "fetch_doc"]
      timeout_seconds: 15
    - name: corp
      transport: sse
      url: http://127.0.0.1:7000/sse
      headers:
        Authorization: "Bearer ${CORP_MCP_TOKEN}"
        X-Tenant: "main"
      allow_methods: ["list_employees"]
```

`headers` の値は `${ENV_VAR}` 形式の環境変数展開のみを許可します。プレーンテキストでの秘密値直書きは config loader で reject します。展開は `internal/secret/env.go` の resolver を再利用します。

### 5.3 公開インターフェース

```go
package mcp

type Transport interface {
    Send(req []byte) error
    Recv() ([]byte, error)
    Close() error
}

type Client struct{...}

func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error)
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage) (Response, error)

type MCPTool struct{...}

func RegisterMCPServers(ctx context.Context, cfg []ServerConfig, reg tool.Registry) error
```

### 5.4 データフロー

1. main.cmdServe や cmdRun の起動時に `RegisterMCPServers` を呼び、利用可能なツールを取得します。
2. ツール名は `mcp:<server>:<method>` のような名前空間で衝突を避けます。
3. Tool.Execute は MCP Call を発行し JSON-RPC のレスポンスを Result に変換します。
4. `allow_methods` に含まれないメソッドは登録時に除外します。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 12.1 | RED | stdio transport の単体テスト（mock server） |
| 12.2 | GREEN | transport_stdio.go を実装 |
| 12.3 | RED | SSE transport の httptest テスト |
| 12.4 | GREEN | transport_sse.go を実装 |
| 12.5 | RED | Discovery の allow_methods 絞り込みテスト |
| 12.6 | GREEN | discovery.go を実装 |
| 12.7 | RED | MCPTool.Execute のエラー伝播テスト |
| 12.8 | GREEN | mcp_tool.go を実装 |
| 12.9 | REFACTOR | E2E `tests/e2e/12-mcp-discovery.sh` |

## 7. テスト計画

### 7.1 ユニット

- JSON-RPC リクエスト ID の連番が衝突しないこと。
- timeout_seconds 超過で Call が cancel されること。

### 7.2 統合

- Go で書いた最小 MCP サーバを fixture とし、agent から `tools` サブコマンドを呼ぶと動的に検出されること。

### 7.3 E2E

`tests/e2e/12-mcp-discovery.sh` でリポジトリ内の `tests/e2e/fixtures/mcp_echo_server/` (main.go 配下) をビルドして stdio で起動し、内部ユニットテストで tools/list と tools/call を検証します。`agent` バイナリ経由の統合検証は将来フェーズで追加予定です。

## 8. ロールアウト

`tools.mcp_servers` 未設定時は何もしません。secret_env のように URL や command も値をそのまま書きますが、シェル展開は行いません。

## 9. リスクと対策

- 任意のコマンド起動は security 上のリスクが高いため、`allow_methods` 未指定は config validation で fatal エラーとして拒否し、起動を阻止します (warn では設定漏れが本番に持ち込まれるリスクがあるため fail-fast に倒す)。
- MCP サーバが固まったときに agent が起動できなくなる懸念があるため、起動時の timeout を厳密に設定します。

## 10. 完了基準

- mcp パッケージのカバレッジ 80 パーセント。
- E2E 成功。
- README に MCP の使い方を新節として追加します。
