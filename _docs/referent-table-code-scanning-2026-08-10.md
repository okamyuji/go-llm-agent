# Code scanning 7件の参照対象と本文用語の対応表

| 確認した対象 | 本文で使う用語 | 本文で述べる判断 |
| --- | --- | --- |
| GitHub code scanning alert #1、`internal/tool/http.go` のユーザー指定URLから `http.Client.Do` までの経路 | HTTP送信先境界 | `http_fetch` が接続できる送信先を、正確なHTTP(S)スキーム、ドメイン許可、private/local IP拒否で制限する |
| `http.Client` が自動追従する3xx応答 | リダイレクト先の再検証 | 最初のURLだけでなく、各リダイレクト先にも同じ送信先制限を適用する |
| `privateDenyDialer` が検証後にホスト名を再度名前解決していた処理 | DNS再解決の競合窓 | 検証済みIPアドレスへ直接接続し、検証と接続の間で別IPへ切り替わる経路をなくす |
| `deny_private_networks: false` と空の `allow_domains` を同時に使える既存設定 | private/local接続の明示許可 | private/local接続を許す場合は対象ホストの `allow_domains` 明示を必須とし、空の許可一覧ではpublic IPだけを許す |
| GitHub code scanning alert #2と#3、`FSRead.Execute` の `os.Lstat` と `os.Open` | ルート固定読み取り | 検証済み許可ルートから `os.Root` を開き、ルート相対名で検査と読み取りを行う |
| GitHub code scanning alert #4から#6、`FSWrite.Execute` の `os.Lstat`、`os.MkdirAll`、`os.WriteFile` | ルート固定書き込み | ディレクトリ作成と書き込みを同じ `os.Root` の内側に固定し、シンボリックリンクで許可ルート外へ移れないようにする |
| GitHub code scanning alert #7、`loadGitIgnore` の `os.ReadFile(filepath.Join(root, ".gitignore"))` | ルート固定検索 | `.gitignore` と検索対象を `os.Root.FS()` から読み、検索開始位置に由来するOSパスを直接ファイルAPIへ渡さない |
| `Sandbox` の強制denyパターンと `search_files` の再帰走査 | 検索対象ごとのdeny適用 | 検索開始位置だけでなく、走査中の各ファイルとディレクトリにも強制denyを適用する |
| `scripts/quality-gate.sh`、E2E variant、`scripts/verify-hardening.sh`、mainへのPush後のcode scanning | 解消確認 | ローカル3系統の検証成功と、main上のopen alertが0件になることを完了条件とする |
