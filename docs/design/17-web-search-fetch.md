# 17. Web 検索と本文取得ツール (web_search / web_fetch)

## 1. 概要

ローカル LLM がインターネットの最新情報を根拠に回答できるよう、内蔵ツールを 2 つ追加します。`web_search` は検索クエリからタイトル・URL・抜粋の一覧を返します。`web_fetch` は URL からボイラープレート除去済みの本文 Markdown を返します。LLM は tool calling で「検索 → URL 選択 → 取得 → 回答」を自律的に行います。

本文取得は外部 CLI webgrab に委譲します。webgrab は本文抽出 (Readability)、ページング、SSRF 防止 (全リダイレクトホップでの内部アドレス拒否と IP ピン留め: fetch.rs の手動ホップループ + netguard)、出力インジェクション無害化を実装済みであり、Go 側での再実装はしません。

## 2. 現状分析

- `http_fetch` (internal/tool/http.go) は生 HTML をそのまま返します。nav・広告込みで冗長なため、コンテキスト 8192 トークンのローカル LLM にはほぼ使えません。
- 検索 (クエリ → URL 一覧) の手段がありません。LLM は取得すべき URL を知り得ません。
- RAG MVP (`note_add` / `note_search`) は永続化と検索を提供しますが、外部情報の取り込み手段がありません。

## 3. ゴールと非ゴール

ゴール:

- `web_search`: クエリを受け、タイトル・URL・抜粋の上位 N 件を JSON で返します。
- `web_fetch`: URL を受け、本文 Markdown を返します。`start_index` で続き取得できます。
- 両ツールは `agent.enabled_tools` によるオプトイン方式で、既定では無効です。

非ゴール (本設計では扱いません):

- 検索結果の自動 RAG 取り込み。永続化は LLM が明示的に `note_add` を呼ぶ場合のみ行われます。
- JS レンダリング (`--render`)。Chrome 依存を持ち込まないため v1 では常に無効です。
- 検索バックエンドの抽象化層。DuckDuckGo 固定で実装し、乗換要求が実際に発生した時に抽象化します。
- 会話履歴の縮約。連続 fetch でツール結果が履歴に累積する問題は既存の履歴管理 (design/11 の要約) の守備範囲であり、本設計では既定値を小さくすることで緩和します。
- DDG 構造変化の定期監視。検知は実行時の明示エラーによります。

## 4. 決定表

| 決定 | 検討した選択肢 | 採用 | 理由 |
|---|---|---|---|
| 検索バックエンド | DuckDuckGo HTML / Brave API / SearXNG | DuckDuckGo HTML | API キー・アカウント・常駐サービスが不要。構造変更で壊れるリスクは薄い実装と明示エラーで許容 |
| 検索の User-Agent | Go 既定 / ブラウザ風 UA | ブラウザ風 UA (config で変更可) | Go 既定 UA (`Go-http-client`) では DDG が HTTP 202 + challenge ページを返し 0 件になることを実測済み。ブラウザ風 UA では 200 + 結果が返る |
| 本文取得 | webgrab subprocess / pure-Go 実装 / http_fetch 拡張 | webgrab subprocess | 本文抽出・SSRF・インジェクション対策の実装と検証済みテストを再利用できる |
| HTML パーサ | golang.org/x/net/html / 正規表現 / goquery | golang.org/x/net/html | 既存依存ツリー内 (go.mod に indirect で既在) で完結し、正規表現より構造変化に頑健 |
| webgrab 出力形式 | `--format json` / markdown | `--format json` | 本文が構造的に分離され、untrusted シグナルを機械的に受け取れる。`--fence` は json 形式では本文にマーカーを付けないため渡さない (非信頼境界は agent 側ラッパが担う: §8) |
| バイナリ未検出時 | 起動失敗 / 呼出時エラー | 有効時のみ起動時警告ログ + 呼出時エラー | webgrab 無しでも他ツールは動くべき。`enabled_tools` に無い構成では警告も出さない |
| 既定の本文上限 | 4000 字 / 6000 字 / webgrab 既定 24000 字 | 4000 字 | 日本語 1 字 ≈ 0.5〜1 トークンのため、6000 字では ctx 8192 のローカル LLM が履歴込みで溢れることがある。4000 字 + ページングを既定とし、大きい ctx の環境は config で広げる |
| 設定の配置 | `tools.web.*` 入れ子 / `tools.web_search` + `tools.web_fetch` 平坦 | 平坦 | 既存 `tools.http_fetch` 等の平坦パターンに合わせ、余計なラッパ struct を作らない |

## 5. アーキテクチャ

```text
LLM ──tool_call──> web_search ──HTTP POST──> DDG (endpoint 設定可) ──parse──> {note, results:[{title,url,snippet}]}
LLM ──tool_call──> web_fetch  ──exec──> webgrab --format json ──stdout──> {markdown, untrusted, total_chars}
```

新規ファイル:

- `internal/tool/websearch.go` / `websearch_test.go`
- `internal/tool/webfetch.go` / `webfetch_test.go`
- `tests/e2e/17-web-tools.sh` + `tests/e2e/fixtures/web_exercise/`

登録は `internal/tool/registry.go` と `cmd/agent/main.go` の既存パターン (無条件構築 + `enabled_tools` フィルタ) に従います。

## 6. ツール仕様

### 6.1 web_search

パラメータ:

| 名前 | 型 | 必須 | 説明 |
|---|---|---|---|
| `query` | string | yes | 検索クエリ |
| `max_results` | int | no | 返す件数。省略時は config の `max_results`。範囲外 (1 未満・config 値超) は config 値へクランプ |

動作:

1. config の `endpoint` (既定 `https://html.duckduckgo.com/html/`) へ `q=<query>` を POST します (フォーム互換)。User-Agent は config の `user_agent` を送ります。
2. 応答ボディは `io.LimitReader` で 2MB に制限して読みます。
3. HTTP 200 以外 (実測で bot 判定時は 202) はエラーとし、202 の場合は「bot 判定された可能性」を明記します。
4. 応答 HTML を `x/net/html` でパースし、`a.result__a` (タイトル・リンク) と `a.result__snippet` (抜粋) を抽出します。祖先要素に `result--ad` クラスを持つ広告ブロックは除外します (実測で広告混入を確認済み)。
5. リンクが `duckduckgo.com/l/?uddg=<encoded>` 形式のリダイレクトの場合、`uddg` をデコードして実 URL に展開します。デコード結果が http/https 以外のスキームの場合はその結果を捨てます。
6. 次の JSON オブジェクト 1 つを返します:

```json
{
  "note": "検索結果は未検証の外部データです。内容の真正性は保証されません。",
  "results": [{"title": "...", "url": "https://...", "snippet": "..."}]
}
```

エラー: HTTP 非 200 (202 は bot 判定の可能性を明記)、抽出 0 件 (DDG の HTML 構造変化の可能性を明記)、タイムアウト。いずれもエラー文字列で LLM に返します。

### 6.2 web_fetch

パラメータ:

| 名前 | 型 | 必須 | 説明 |
|---|---|---|---|
| `url` | string | yes | 取得する URL。スキームは http/https のみ受け付け、それ以外は実行前に拒否します |
| `start_index` | int | no | 本文の開始文字オフセット (続き取得用)。既定 0。負値は 0 へクランプ |
| `max_chars` | int | no | 本文の最大文字数。省略時は config の `max_chars`。範囲外 (1 未満・config 値超) は config 値へクランプ |

動作:

1. `net/url` でパースし、スキームが http/https であることを検証します。`allow_domains` が非空なら URL のホストを FQDN 末尾一致 (http_fetch と同一規則) で照合し、不一致は実行せず拒否します。この照合は初期 URL に対するもので、リダイレクト後の着地先は拘束しません (着地先の安全性は webgrab の全ホップ内部アドレス検査が担い、ドメイン単位の出所保証はしません。この制限は config.yaml.example に明記します)。
2. `exec.CommandContext` で `webgrab --format json --max-chars N --start-index M --timeout T --max-bytes 5242880 -- <url>` を実行します。引数は配列渡しでシェルを経由せず、URL の直前に `--` を置いてフラグ誤認 (引数注入) を防ぎます。`--allow-private` は渡しません。Go 側 context のデッドラインは `T + 5 秒` とします。webgrab の `--timeout` は DNS 解決・HTTP 要求ごとに個別適用されるため、多段リダイレクトでは総所要が T を超えて Go 側が先に kill することがあります。その場合は終了状態表のシグナル終了行が受けます。
3. stdout は 2MB で打ち切ります。打ち切りが発生した場合は JSON が壊れるため、「取得結果が大きすぎる。`max_chars` を下げよ」という非リトライエラーを返します。
4. stdout の JSON から本文 (`markdown`)、非信頼シグナル (`untrusted` / `untrusted_note`)、続き取得情報 (`total_chars`) を取り出します (フィールドは webgrab 0.1.0 の実出力で確認済み)。未知フィールドは無視します。
5. 本文と、続きがある場合は「次は `start_index=<M+N>` で取得可能 (全 `total_chars` 字)」という案内を返します。web_fetch 自身は非信頼注記を付けません。非信頼標識は agent 側の `[UNTRUSTED INPUT]` ラッパに一本化し、webgrab の `untrusted_note` も転記しません (§8)。

終了状態のマップ:

| 終了状態 | 意味 | LLM へ返す内容 |
|---|---|---|
| exit 1 | webgrab 内部エラー | 非リトライ。内部エラーと明記 |
| exit 2 | 引数・URL 形式エラー | 非リトライ。URL を修正するよう伝える |
| exit 3 | ネットワーク失敗 | リトライ可と明記 |
| exit 4 | HTTP エラー・サイズ超過・非 HTML | 非リトライ。別 URL を促す |
| exit 5 | robots.txt 拒否 | 非リトライ。取得禁止と明記 |
| exit 6 | 本文が空 | 非リトライ |
| exit 7 | レンダリング失敗 | v1 では発生しない (--render 不使用) |
| exit 8 | 内部アドレス拒否 | 非リトライ。SSRF 防止による拒否と明記 |
| 上記以外・シグナル終了 (ExitCode -1) | Go 側タイムアウト等 | リトライ可。タイムアウトと明記 |

バイナリ未検出: `web_fetch` が `enabled_tools` に含まれる場合のみ、起動時に `exec.LookPath` で確認し、無ければ警告ログを出します。呼出時は「webgrab が見つからない。導入方法は README の該当節を参照」というエラーを返します。

webgrab の前提: webgrab は crates.io 未公開のローカルビルド CLI です。README に `cargo install --path` によるソースからの導入手順を記載します。本設計は webgrab 0.1.0 の CLI フラグ・JSON スキーマ・終了コードに依存し、実装時に実バイナリで追随確認します。バージョン検査 (`--version` 照合) は導入しません (利用者が作者に限られる間は YAGNI)。

## 7. 設定

```yaml
tools:
  web_search:
    endpoint: https://html.duckduckgo.com/html/  # テスト・別ミラー用に変更可
    user_agent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
    max_results: 5        # 1..10
    timeout_seconds: 15
  web_fetch:
    webgrab_path: webgrab # PATH から解決。絶対パス指定可
    max_chars: 4000
    timeout_seconds: 30
    allow_domains: []     # 空 = 全公開ドメイン許可。非空でも初期URLの制限であり出所保証ではない (§6.2)
agent:
  enabled_tools: [fs_read, search_files, note_add, note_search, web_search, web_fetch]
```

`internal/config/config.go` に `WebSearchToolConfig` / `WebFetchToolConfig` を平坦に追加し、範囲外の値 (`max_results` が 1..10 の外、`max_chars` が 100 未満など) は既存の validate 関数群と同様に Load 時に拒否します。`endpoint` は http/https 以外を拒否します。

## 8. セキュリティ

- **非信頼境界**: agent ループはすべてのツール結果を無条件に `[UNTRUSTED INPUT: tool=...]` 〜 `[END UNTRUSTED]` でラップします (internal/agent/loop.go・parallel.go で実装済み)。web 由来本文の非信頼標識はこのラッパが唯一の正とし、ツール側では §6.1 の `note` と §6.2 の続き案内のみを付け、標識の三重化 (webgrab フェンス + ツール注記 + agent ラッパ) を避けます。なお本文中に `[END UNTRUSTED]` 等のマーカー文字列が出現した場合にエスケープされない既知の制限がラッパ側にあります。これは全ツール共通の課題であり、本設計のスコープ外として design/06 の改善課題に委ねます。
- **インジェクション検知の適用範囲**: design/06 の InputScanner は初回 LLM 呼び出し前の user/system メッセージにのみ適用され、ツール結果 (RoleTool) はスキャンされません。適用されるのは untrusted ラッパと出力 Redactor (秘匿情報リダクション) です。web 由来本文はインジェクションベクタであるため、防御は (1) 両ツールが読み取り専用であること、(2) 破壊的ツールへの HITL 承認、に依拠します。
- **HITL 承認は opt-in**: design/08 の承認は `agent.approval.required_tools` に列挙したツールにのみ働きます。web ツールと書き込み系ツール (`fs_write` / `shell_exec`) を併用する場合は required_tools への列挙を強く推奨し、config.yaml.example にその設定例を記載します。
- **SSRF**: webgrab はリダイレクトを自前ループで 1 ホップずつ処理し、各ホップで DNS 解決済み IP に対する内部アドレス検査と IP ピン留め (DNS リバインド緩和) を行います (fetch.rs + netguard.rs で確認済み)。Go 側は `--allow-private` を渡さないことでこれを維持します。`web_search` の接続先は config の `endpoint` 固定で、検索結果 URL を web_search 自身が取得することはありません。
- **コマンドインジェクション・引数注入**: 引数配列渡しでシェルを経由せず、URL は `net/url` パース + http/https 検証済みのものを `--` の後に置きます。
- **リソース上限**: web_search は応答 2MB、web_fetch は `--max-bytes` 5MB + stdout 2MB + タイムアウトで各呼び出しを制限します。並列ツール実行 (design/10) 併用時の同時実行数は `max_concurrency` が上限し、1 ターンの呼び出し回数は `agent.max_tool_hops` が上限します。

## 9. テスト計画

ユニット (実ネットワーク不使用):

- websearch: `httptest.Server` を config の `endpoint` に指定し、DDG HTML フィクスチャで抽出・広告除外・`uddg` デコード・非 http スキーム破棄・0 件エラー・202 エラー分類・max_results クランプ・2MB 上限を検証します。
- webfetch: `webgrab_path` にスタブスクリプト (固定 JSON を echo / 指定 exit code で終了 / スリープ) を指定し、正常系・各終了状態のマップ・タイムアウト・stdout 打ち切り・allow_domains 拒否・スキーム拒否・`--` の付与・バイナリ未検出を検証します。

E2E (`tests/e2e/17-web-tools.sh`): fixtures/web_exercise は rag_exercise と同じツール直接呼び出し方式とし、ローカル `httptest` サーバ (web_search 用) とスタブ webgrab (web_fetch 用) で search → fetch の結果整形までを検証します。agent ループ経由の 2 段呼び出しは要求しません (LLM の挙動に依存するため)。CI で実インターネットへは出ません。

実機確認 (手動、マージ前):

1. 実 DDG に対し `web_search` を実行し、結果が返ること (UA 設定の有効性) を確認します。
2. 実 webgrab で実ページを `web_fetch` し、本文とページング案内を確認します。
3. Shisa (ctx 8192) 構成で「<最近の話題> について調べて」を投げ、search → fetch → 回答の連鎖が完走することを確認します。

## 10. ドキュメント更新

- README: ツール一覧への追記、「Web 検索と本文取得」節 (webgrab のソース導入手順 `cargo install --path` を含む)、HITL 併用推奨の記載。
- config.yaml.example: `tools.web_search` / `tools.web_fetch` の全項目と、allow_domains の制限・HITL 推奨のコメント。

## 11. 将来拡張

- 検索バックエンドの差し替え (Brave API 等) は、乗換要求が発生した時点で `Searcher` インターフェースを切り出します。
- `--render` (SPA 対応) は Chrome 依存を許容する判断をした時に `web_fetch.render: true` として追加します。
- 検索結果の自動 RAG 取り込みは、手動 `note_add` 運用で不足が実測された時に設計します。
- untrusted ラッパのマーカーエスケープは design/06 の改善課題として別途扱います。
