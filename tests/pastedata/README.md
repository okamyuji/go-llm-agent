# pastedata

REPLのペースト処理を検証するためのサンプルデータです。結合テスト
(`internal/transport/cliui/lineedit/paste_test.go`のfixture round-tripと
`internal/transport/cliui/repl_paste_pty_test.go`)が読み込みます。
マージ後の手動確認にもそのまま使えます。

## 期待動作

- 改行を含む、または80文字を超えるペーストは`[pasted #N M行]`と短縮表示されます
- Enterで原文が1プロンプトとして送信されます
- ペースト中のCtrl-Cは取り込みを破棄します
- 生成中に貼り付けても行単位の誤発火は起きません

## ファイル一覧

| ファイル | 内容 | 確認する症状 |
|---|---|---|
| agent-output-with-prompt.txt | `>>`プロンプト行を含むagent出力のコピー | 各行が個別プロンプトとして誤発火しないこと |
| multiline-plain.txt | 40行のプレーンテキスト | 行単位分割の再発防止 |
| long-single-line.txt | 5200文字の1行 | 4096文字の行長上限で切り捨てられないこと |
| ansi-escape-log.txt | ANSIエスケープ列と末尾の単独ESCバイトを含むログ | ペースト後に/quitとCtrl-Cが効き続けること |
| japanese-mixed.txt | 日本語・全角混在 | 表示幅処理と内容の保全 |
| crlf-lines.txt | CRLF改行 | 改行が二重にならないこと |

## 手動確認の手順

1. `bin/agent`を端末で起動します
2. 各ファイルの内容をコピーしてプロンプトへ貼り付けます
3. `[pasted #1 M行]`の短縮表示を確認し、Enterで送信します
4. 応答生成中にもう一度貼り付け、生成完了後に1プロンプトへまとまることを確認します
5. ansi-escape-log.txtの貼り付け後に`/quit`でREPLが終了できることを確認します

## 既知の制限

- 短縮トークンを手で編集した場合は、編集後の文字列がそのまま送信されます
- 履歴(上矢印)には短縮表示のまま残り、呼び出して送信するとトークン文字列が送られます
- 生成中に貼り付けた本文中の単独ESCバイトは中断キーとして解釈されることがあります
