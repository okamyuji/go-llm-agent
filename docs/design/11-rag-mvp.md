# 11. 短期要約とノート全文検索による RAG MVP

## 1. 概要

セッション内の長い会話を自動要約して system プロンプト先頭に差し込み、永続ノートを `note_add` と `note_search` ツールで管理できる最低限の RAG 機能を実装します。ベクター DB は将来拡張のための差し込みポイントだけ用意します。

## 2. 書籍根拠

Ch6「Foundational Approaches to Memory」「Note-Taking」「Retrieval-Augmented Generation」を参照します。書籍は全文検索とベクター検索を併用する重要性、ノートテイクの効用を説いています。

## 3. 現状分析

`internal/storage/session.go` は会話を JSONL に追記するだけです。`chat` REPL では履歴を読み戻さず、要約も注入していません。ノートを保存・検索するツールも未実装です。

## 4. ゴール

- セッション履歴が一定トークン数を超えると自動要約します。
- `note_add` と `note_search` ローカルツールが提供されます。
- session を再開したとき直近 N 件 + 要約 + 関連ノートが system プロンプトに差し込まれます。
- 将来の vector backend をプラグインできるインターフェースを設けます。

## 5. 設計

### 5.1 アーキテクチャ概要

`internal/memory` パッケージを新設し、`Summarizer`、`NoteStore`、`Retriever` の 3 抽象を定義します。Summarizer は LLM を使い、NoteStore は MVP として JSONL ファイル (`internal/memory.FileNoteStore`) を用いた追記型ストアで全文検索を提供します。Retriever は近似類似度を Bag-of-Words + 重み付きスコア (title=3, tags=2, body=1) で計算します。将来フェーズで SQLite + FTS5 やベクター DB に差し替え可能なよう、`NoteStore` を interface として切り出しておきます。

検索クエリのトークナイズは ASCII 範囲の語については空白・句読点分割をそのまま採用し、非 ASCII (CJK 等の分かち書きしない言語) を含むトークンは rune 単位の 2-gram に展開します。これにより日本語の Title/Body もクエリと部分一致でヒットさせます。形態素解析や FTS5 への置き換えは将来フェーズで対応します。

### 5.2 設定スキーマ

```yaml
storage:
  # MVP では JSONL 追記ファイルを使う
  notes_path: ~/.local/state/go-llm-agent/notes.jsonl
memory:
  summary:
    enabled: true
    trigger_tokens: 4000
    target_tokens: 500
    model: openai/gpt-4o-mini
  retrieval:
    top_k: 5
```

MVP の `storage.notes_path` 未指定時は `storage.sessions_dir/notes.jsonl` へフォールバックします。SQLite + FTS5 への切り替えは将来フェーズで `memory.notes.store: sqlite` のような設定を追加して実装します。

`trigger_tokens` と `target_tokens` の計測は MVP では「メッセージごとの UTF-8 byte 数 / 4 をモデル非依存の概算トークン数として加算する」方針で実装します。対象は system / user / assistant メッセージの `Content` 文字列で、tool_call の Arguments は含めません。複数モデルを跨いで会話する場合でも閾値判定は同じ概算式で行い、要約 LLM の選定だけ `memory.summary.model` 設定に従います。将来 OpenAI 系の tiktoken やプロバイダ別の正規トークナイザを差し込めるよう、`tokenCounter` を interface として `internal/memory` に切り出す予定です。

### 5.3 公開インターフェース

```go
package memory

type Note struct { ID string; Title string; Body string; Tags []string; CreatedAt time.Time }

type NoteStore interface {
    Add(ctx context.Context, n Note) error
    Search(ctx context.Context, query string, topK int) ([]Note, error)
}

type Summarizer interface {
    Summarize(ctx context.Context, history []llm.Message) (string, error)
}

type Retriever interface {
    Retrieve(ctx context.Context, query string, topK int) ([]Note, error)
}
```

### 5.4 データフロー

1. agent.Service.Run の冒頭で session_id と直近メッセージから query を作り、Retriever.Retrieve を呼びます。
2. 取得ノートを `[KNOWLEDGE]` セクションとして system プロンプト末尾に追加します。
3. 履歴が trigger_tokens を超えたら Summarizer を非同期実行し、要約を session メタに保存します。Summarizer は agent.Service が保持する rootCtx を `context.WithoutCancel` で派生させた別 ctx と専用 goroutine で動かします。エラーが返った場合は slog.Warn でログするのみで Run 本流を阻害しません。goroutine は要約完了か agent.Service.Close からの shutdown channel 受信のいずれかで終了するよう設計し、go.uber.org/goleak が検知できないようテストで保証します。
4. `note_add` ツールが呼ばれたら NoteStore に保存します。
5. `note_search` ツールは Search を直接呼びます。

## 6. 実装タスク（TDD）

| # | フェーズ | 作業 |
| --- | --- | --- |
| 11.1 | RED | NoteStore Add と Search のテスト |
| 11.2 | GREEN | MVP として JSONL ベースの FileNoteStore を実装 (SQLite + FTS5 は将来フェーズ) |
| 11.3 | RED | Summarizer の fake LLM テスト |
| 11.4 | GREEN | summarizer.go を実装 |
| 11.5 | RED | Retriever の BM25 ランキングテスト |
| 11.6 | GREEN | retriever.go を実装 |
| 11.7 | RED | agent.Service の system 注入テスト |
| 11.8 | GREEN | 09 番が先に実装済みの場合は `internal/agent/strategy/react.go` を改修し、09 番が未実装の場合は `internal/agent/loop.go` を改修する（PR 着手時点でファイル存在を確認し、両方ある場合は前者を優先） |
| 11.9 | REFACTOR | tools/note_add と tools/note_search を実装し E2E 追加 |

## 7. テスト計画

### 7.1 ユニット

- JSONL FileNoteStore が複数の Add でレコードを追記すること。
- Retriever の top_k がスコア順に返ること。

### 7.2 統合

- chat REPL でノート登録 → 別セッションで検索 → 関連ノートが system に注入されることを確認。

### 7.3 E2E

`tests/e2e/11-rag-mvp.sh` で `note_add` と `note_search` の呼び出しを fixture バイナリ経由で実行し、JSONL ファイルに追記されたレコードを `grep` で確認します。SQLite + FTS5 を導入した将来フェーズでは `sqlite3 :path: "SELECT count(*) FROM notes"` 形式の検証に切り替える想定です。

## 8. ロールアウト

MVP は JSONL ベースのため新規依存を追加しません。将来 SQLite に切り替える際は `modernc.org/sqlite` の採用を想定しています。`memory.summary.enabled=false` の場合は要約なしで現状互換です。

## 9. リスクと対策

- JSONL ファイルの権限を 0600、親ディレクトリを 0700 にして誤って公開しないようにします。SQLite に切り替える将来フェーズでも同じ権限制御を継承します。
- Summarizer の余計な LLM 呼び出しでコストが増えるため、trigger_tokens に達したときだけ実行します。
- JSONL の線形スキャンは大規模運用には不向きです。本番運用フェーズではインデックス付きストアへの切り替えを優先課題とします。

## 10. 完了基準

- memory パッケージのカバレッジ 80 パーセント。
- E2E 成功。
- README に Memory 節を追加します。
