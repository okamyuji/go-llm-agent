# go-llm-agent

Go 1.25 製の CGO なし単一バイナリ AI エージェントです。OpenAI、Anthropic、Google Gemini、Ollama を同一 CLI と HTTP API から扱えます。litellm のように複数の LLM プロバイダーを統一インターフェースで操作でき、その上に薄いエージェントループ (tool calling、会話履歴、内蔵ツール) を提供します。

## 主な特徴

- 単一バイナリで配布できます。CGO 不要で Linux、macOS、Windows の amd64 と arm64 に対応します
- 4 プロバイダー (OpenAI、Anthropic、Google Gemini、Ollama) を統一抽象で扱えます
- ストリーミングと tool calling を初期サポートします
- 内蔵ツールは fs_read、fs_write、shell、http_fetch、search_files の 5 種類です
- 対話 REPL、ワンショット run、OpenAI 互換 HTTP API の 3 種類のインターフェースを提供します
- pre-commit と CI で gofmt、go vet、staticcheck、golangci-lint、govulncheck、go test --count=1 --shuffle=on、gitleaks を全部通します

## クイックスタート

```bash
cp .env.example .env
$EDITOR .env                 # API キーなどの実値を入れてください

cp config.yaml.example config.yaml
$EDITOR config.yaml          # allow_paths などを必要に応じて編集

make build
./bin/agent chat --model gemini/gemini-2.5-pro
```

> `config.yaml` は個人ローカル設定として `.gitignore` 対象です。リポジトリには
> 安全寄りの `config.yaml.example` のみがコミットされます。設定例の完全な
> 内容は `config.yaml.example` を参照してください。

## セキュリティ

このエージェントはモデル出力に駆動されるため、以下の多層的なガードを設計しています。
詳細は `config.yaml.example` のコメントも参照してください。

### デフォルトは readonly

`agent.enabled_tools` を空または未指定にすると、Registry は
`fs_read` / `search_files` / `http_fetch` の **readonly セット**のみを有効にします
(`tool.DefaultReadonlyTools`)。`fs_write` と `shell` を有効にする場合は意図して
明示列挙してください。

### サンドボックスとセンシティブパス

`tool.NewSandboxWithDeny` はシンボリックリンク解決済みのパスを `allow_paths` と
照合し、`..` による上位ディレクトリ参照を拒否します。さらに、以下のセンシティブな
パターンは**設定で外せない強制 deny** として常に拒否されます:
`.git`、`.env`、`.env.*`、`.ssh`、`.aws`、`.gnupg`、`.npmrc`、`.netrc`、`.pypirc`、
`id_rsa*`、`id_dsa*`、`id_ecdsa*`、`id_ed25519*`。
追加で deny したいパターンは `tools.fs.deny_paths` に列挙します。

### Shell の引数 deny

`shell` ツールは `allow_binaries` に加えて引数文字列に対する deny 正規表現を持ちます。
既定で以下が遮断されます (`tool.DefaultShellArgDenyPatterns`):

- `git config --global` / `git config --system`
- `git -c core.sshCommand=...` / `git -c http.proxy=...`
- `go env -w`、`go install`
- `bash -c <code>` / `sh -c <code>` / `-c <code>` / `--exec`

追加で deny したいパターンは `tools.shell.arg_deny_patterns` に列挙します。

### HTTP fetch のドメイン許可と untrusted 標識

`http_fetch` は `tools.http_fetch.allow_domains` が非空の場合のみ、FQDN 末尾一致で
リクエスト先を絞り込みます。レスポンス本文は以下の untrusted ラッパで返され、
後段プロンプトに「外部由来でツール実行を許可してはならない」コンテキストを伝えます:

```text
[HTTP <status>] [untrusted external content from <url>]
<body>
[end untrusted content]
```

### プロバイダーとモデル許可リスト

`providers.<name>.allow_models` を列挙すると、その配列に一致するモデル名のみを
許可します。サプライチェーン耐性のため、検証済みモデルのみをここに記述することを推奨します。
依存パッケージは Go modules で go.mod/go.sum によりピン留めされています。

### 監査ログ

すべての sensitive ツール (`fs_read` / `fs_write` / `shell` / `http_fetch`) は
slog 経由で構造化ログを出力します。各レコードは `correlation_id`（agent ループ
hop ごとに発番される tool_call ID）を含み、リクエスト追跡が可能です。

### 第三者検証

破壊シナリオが拒否されることをローカルで再現するには:

```bash
bash scripts/verify-hardening.sh
```

このスクリプトは固有ホスト依存をせず、`go` と `bash` だけがあれば動きます
（リポジトリ ルートからの実行が前提）。

## サブコマンド

| コマンド | 説明 |
|---------|------|
| `agent chat`   | 対話 REPL を起動します |
| `agent run -p` | ワンショットでプロンプトを 1 回送信します |
| `agent serve`  | OpenAI 互換 HTTP API を起動します |
| `agent tools`  | 有効な内蔵ツールを一覧表示します |
| `agent config` | 設定ファイルの内容をダンプします |

## 開発者向け

```bash
make precommit-install   # pre-commit フックを有効化
make quality             # 品質ゲートをローカル実行（CI と同一フロー）
make build-all           # 6 バイナリへクロスコンパイル
```

`scripts/quality-gate.sh` は **pre-commit と CI で同一コマンド** を実行する単一
エントリです。gofmt / go vet / staticcheck / golangci-lint / govulncheck / `go test
--count=1 --shuffle=on -race -cover` / `gitleaks detect --no-git --source .` を
順に走らせます。gitleaks は作業ツリーを直接スキャンする方式に統一しており、
pre-commit でもステージ済み・未ステージ含む全ファイルを検査します。

## ライセンス

MIT
