# Code scanning 7件に対する境界強化

対応表は `referent-table-code-scanning-2026-08-10.md` に保存した。

## HTTP送信先境界

`http_fetch` はモデルが指定したURLへ接続するため、URL全体を信頼できる入力として扱わない。受理するスキームは `http` と `https` の完全一致に限定し、ホスト名がないURLとuserinfoを含むURLは拒否する。

private/local IP拒否を有効にした通常構成では、DNSで得た全IPを検査し、private、loopback、link-local、multicast、unspecifiedのいずれかを含むホストを拒否する。検査後はホスト名を再解決せず、検証済みIPへ直接接続する。これにより、検査時と接続時でDNS応答を切り替える経路をなくす。

private/local接続を必要とするテストまたは明示構成では、`deny_private_networks: false` だけでは許可しない。接続先ホストを `allow_domains` に明示した場合に限って許可する。空の `allow_domains` はpublic IPへの任意接続を意味し、private/local接続の許可には使わない。

HTTPクライアントが3xxを追従するたびに、スキーム、ホスト、ドメイン許可を再検証する。private/local IPの検査は各接続のdial時に行う。このため、許可済みURLから許可外ドメインやprivate/local IPへリダイレクトして制限を迂回できない。

## ルート固定読み取り、書き込み、検索

`Sandbox.CheckPath` は入力パスの正規化、許可ルートとの照合、強制denyを担当する。ただし、検証後に元の絶対パスを通常の `os` ファイルAPIへ渡す方式では、検証と利用の間にシンボリックリンクを交換する競合窓が残る。

検証後のファイル操作はGo 1.25の `os.Root` に統一する。許可ルートを `os.OpenRoot` で開き、そのルートからの相対名だけを `Root.Lstat`、`Root.Open`、`Root.MkdirAll`、`Root.WriteFile`、`Root.FS` に渡す。`os.Root` はシンボリックリンクを追う場合もルート外参照を拒否するため、検証後のパス交換が許可ルート外アクセスへ変わらない。

`search_files` は開始位置だけでなく、再帰走査中の各ディレクトリとファイルにも `Sandbox.CheckPath` を適用する。強制denyに一致するディレクトリは走査せず、ファイルは読み取らない。

## 解消確認

変更は次の順で確認する。

1. `go test` でURL形式、ドメイン、リダイレクト、private/local IP、シンボリックリンク、強制denyの回帰を確認する。
2. `rtk bash scripts/quality-gate.sh` を実行する。
3. `rtk env RUN_E2E=1 bash scripts/quality-gate.sh` を実行する。
4. `rtk bash scripts/verify-hardening.sh` を実行する。
5. `origin/main` へPushし、GitHub ActionsとCodeQLの完了後にmain上のopen code scanning alertが0件であることをGitHub APIで確認する。
