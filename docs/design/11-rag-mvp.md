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

`internal/memory` パッケージを新設し、`Summarizer`、`NoteStore`、`Retriever` の 3 抽象を定義します。Summarizer は LLM を使い、NoteStore は SQLite + FTS5 で全文検索を提供します。Retriever は近似類似度を Bag-of-Words + cosine で計算します。

### 5.2 設定スキーマ

```yaml
memory:
  summary:
    enabled: true
    trigger_tokens: 4000
    target_tokens: 500
    model: openai/gpt-4o-mini
  notes:
    store: sqlite
    path: ~/.local/state/go-llm-agent/notes.db
  retrieval:
    top_k: 5
```

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
| 11.2 | GREEN | SQLite FTS5 で実装 |
| 11.3 | RED | Summarizer の fake LLM テスト |
| 11.4 | GREEN | summarizer.go を実装 |
| 11.5 | RED | Retriever の BM25 ランキングテスト |
| 11.6 | GREEN | retriever.go を実装 |
| 11.7 | RED | agent.Service の system 注入テスト |
| 11.8 | GREEN | 09 番が先に実装済みの場合は `internal/agent/strategy/react.go` を改修し、09 番が未実装の場合は `internal/agent/loop.go` を改修する（PR 着手時点でファイル存在を確認し、両方ある場合は前者を優先） |
| 11.9 | REFACTOR | tools/note_add と tools/note_search を実装し E2E 追加 |

## 7. テスト計画

### 7.1 ユニット

- FTS5 が日本語の bigram tokenizer で検索可能であること。
- Retriever の top_k がランキング順に返ること。

### 7.2 統合

- chat REPL でノート登録 → 別セッションで検索 → 関連ノートが system に注入されることを確認。

### 7.3 E2E

`tests/e2e/11-rag-mvp.sh` で `note_add` と `note_search` の呼び出しを CLI 経由で実行し、SQLite に保存されたことを `sqlite3 :path: "SELECT count(*) FROM notes"` で確認します。

## 8. ロールアウト

SQLite を新規依存にするため `modernc.org/sqlite` を採用します。`memory.summary.enabled=false` の場合は要約なしで現状互換です。

## 9. リスクと対策

- SQLite ファイルの権限を 600 にし、誤って公開しないようにします。
- Summarizer の余計な LLM 呼び出しでコストが増えるため、trigger_tokens に達したときだけ実行します。

## 10. 完了基準

- memory パッケージのカバレッジ 80 パーセント。
- E2E 成功。
- README に Memory 節を追加します。
