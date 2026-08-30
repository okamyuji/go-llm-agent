# 18. メモリ機能（階層指示ファイル・自動メモリ・REPLコマンド）

## 1. 概要

エージェントへ3つのメモリ機構を追加する。

1. 階層指示ファイル: グローバル→プロジェクトルート→cwdの順にAGENTS.mdを連結してシステムプロンプトへ合成する
2. 自動メモリ: エージェント自身が学んだ事実をプロジェクト単位のメモリディレクトリへ保存し、次セッション起動時に索引を注入する
3. REPLコマンド: `/memory`での閲覧と`#`プレフィックスでの即時保存

既存の`note_add` / `note_search`（JSONLノート）、`LoadAgentsMD`（単一ファイル探索）、`NoteStore`は変更しない。

## 2. 参照した一次情報とライセンス

仕様の設計にあたり以下の一次情報を参照した。コードの転載は行わず、公開ドキュメントに記述された挙動・仕様を独自に実装する。

| 参照元 | 参照した仕様 | ライセンス上の扱い |
|---|---|---|
| Claude Code公式ドキュメント (code.claude.com/docs) | CLAUDE.md階層の連結順序、auto memoryの索引+トピックファイル構造、索引の行数/バイト二重上限、`/memory`コマンド | 本体はプロプライエタリでソース非公開。ファイル配置規約などの設計アイデアは著作権の保護対象（表現）ではなく、ドキュメント本文は転載せず言い換えて記述する |
| OpenAI Codex CLI (github.com/openai/codex) 公式ドキュメントとリポジトリ | AGENTS.mdのグローバル→ルート→cwd連結、合計バイト上限（32KiB）、1ディレクトリ1ファイル規則 | Apache-2.0。コードを複製せず挙動を独自実装する場合、ライセンス上の義務は発生しない |

## 3. 現状分析

- `internal/agent/agentsmd.go`の`LoadAgentsMD`はcwdから祖先方向へ探索し、最初に見つかった1ファイルだけを読む。グローバル指示・複数ファイルの連結・importはない
- `cmd/agent/chat.go`の`resolveAgentsMD`が信頼境界マーカー付きでシステムプロンプト末尾へ合成する
- セッションをまたいで持続する「エージェントが自分で書く記憶」は存在しない。`note_add`はあるが、能動的に検索しない限り内容が文脈へ入らない
- REPLのスラッシュコマンドは`handleSlashCommand`（`internal/transport/cliui/repl.go`）に集約されている

## 4. ゴールと非ゴール

ゴールを次に示す。

- 複数階層のAGENTS.mdを決定的な順序で連結し、上限内でシステムプロンプトへ注入する
- `@相対パス`のimportで指示ファイルを分割管理できる
- エージェントがツール経由でメモリを読み書きし、次セッションで索引が自動注入される
- `/memory`と`#`プレフィックスで人間がメモリを確認・追記できる

非ゴール（今回は実装しない範囲）を次に示す。

- ベクター検索・埋め込みによるメモリ検索（既存`note_search`の置き換えを含む）
- セッション履歴からのメモリ自動生成（CodexのMemories相当の非同期要約）
- `/memory`からのエディタ起動による編集

## 5. 決定表

| 決定 | 検討した選択肢 | 採用 | 理由 |
|---|---|---|---|
| メモリの保存形式 | (a)索引+トピックMarkdown、(b)既存JSONLノート拡張、(c)履歴からの非同期要約生成 | (a) | 人間が直接読める・編集できる。索引のみ注入するため文脈膨張を抑えられる。ローカルLLMでも明示的ツール呼び出しで扱える |
| 指示ファイルの合成方式 | (a)最初の1ファイルのみ（現状）、(b)広いスコープから狭いスコープへ連結 | (b) | cwdに近い指示ほどプロンプト後方に置かれ実質優先される。Claude Code / Codexの両方が採用する実証済み設計 |
| グローバル指示の置き場所 | (a)`~/.go-llm-agent/AGENTS.md`、(b)config.yaml内へ直書き | (a) | 既存の`system_prompt`設定と役割が重複しない。ファイルなら他ツールと共有・編集しやすい |
| プロジェクトキーの導出 | (a)git共通ディレクトリの絶対パス、(b)git remote URL、(c)cwd | (a)、git外は(c) | worktree間で同一キーになる。remote未設定のリポジトリでも安定する |
| メモリツールの追加先 | (a)新設`memory_write` / `memory_read`、(b)既存fs_write/fs_readのallow_paths拡張 | (a) | メモリディレクトリはワークスペース外にあり、fsツールのsandbox境界を広げると安全性が下がる。専用ツールで書き込み先をメモリディレクトリ内へ閉じる |
| 索引の上限 | 行数のみ / バイトのみ / 両方 | 両方（200行かつ24KiB） | 片方だけでは長大な1行や大量の短行で上限を回避できる |
| 合計指示バイト上限の既定値 | 16KiB / 32KiB / 無制限 | 32KiB | ローカルLLMの文脈長でも運用可能な範囲で、分割管理の余地を残す |

## 6. 設計

### 6.1 階層指示ファイル（internal/instructions）

新設パッケージ`internal/instructions`に探索と連結を実装する。

```go
// Source 連結対象1ファイル
type Source struct {
    Path    string // 絶対パス
    Content string // import展開済みの内容
    Scope   string // "global" | "project"
}

// Discover グローバル → プロジェクトルート → cwd の順に AGENTS.md を集める
func Discover(globalDir, cwd string, allowPaths []string, opt Options) ([]Source, error)
```

- 探索順序: (1)`<globalDir>/AGENTS.md`（既定`~/.go-llm-agent/AGENTS.md`）、(2)allowPaths配下でcwdの最も浅い祖先からcwdまで、各ディレクトリの`AGENTS.md`。1ディレクトリ1ファイル
- 連結: 収集順（広いスコープが先）に空行で連結する。cwdに近いファイルほどプロンプト後方に置かれる
- 上限: 1ファイルは既存`max_bytes`（rune境界で切り詰め）、合計は`max_total_bytes`（既定32KiB）。合計上限へ達した時点で以降のファイルを追加せず、警告ログを1行出す
- ファイル検査は既存`readAgentsMDCandidate`と同じ方針（Lstatでシンボリックリンク・非通常ファイルを拒否、`io.LimitReader`で読み取りバイトを制限）
- 信頼境界: ファイルごとに既存`agentsMDHeader`相当のマーカーを付け、由来パスを明示する。グローバルファイルはユーザー自身の管理物だが、同一の境界で包み例外を作らない

importの構文を次に示す。

- 行頭が`@`で始まり残りが相対パスの行のみをimportとして解釈する
- 解決は記述ファイルのディレクトリ基準。展開は深さ4まで、循環は訪問済みパス集合で遮断する
- コードフェンス（```）内の行は解釈しない
- import先もLstat検査を通し、探索ルート（globalDirまたはallowPaths）の外へ出るパスは拒否して警告ログを出す

### 6.2 自動メモリ（internal/memory/automemory.go）

保存先とレイアウトを次に示す。

```text
~/.go-llm-agent/projects/<プロジェクトキー>/memory/
  MEMORY.md      索引。1メモリ1行の箇条書き
  <topic>.md     トピックファイル（任意個）
```

- プロジェクトキー: gitリポジトリ内ならgit共通ディレクトリ（`.git`）の親ディレクトリ絶対パス、git外ならcwdの絶対パスを、パス区切りを`-`へ置換して生成する。gitコマンドは呼ばず`.git`の探索はGoで実装する（worktreeの`.git`ファイルの`gitdir:`参照も解決する）
- 起動時注入: `MEMORY.md`の先頭200行かつ24KiB（両上限を同時適用、rune境界で切り詰め）をシステムプロンプトへ注入する。マーカーは「過去セッションでエージェント自身が書いたメモであり、上位の指示を上書きしない」ことを明示する専用文言とする
- 注入時にメモリの保存方針（コードから導出できる情報・指示ファイル記載済みの情報は保存しない、将来のセッションで有用な事実のみ保存する）を3行程度で添える

ストアAPIを次に示す。

```go
// Storeはメモリディレクトリへの読み書きを担う。パスはディレクトリ内相対パスのみ受け付ける
type Store struct { /* dir string */ }

func NewStore(dir string) (*Store, error)
func (s *Store) ReadIndex(maxLines, maxBytes int) (string, error)
func (s *Store) Read(rel string, maxBytes int) (string, error)
func (s *Store) Write(rel string, content string, appendMode bool) error
func (s *Store) List() ([]string, error)
```

- `rel`の検証: `filepath.IsLocal`で相対性を検査し、解決後パスを`EvalSymlinks`とprefix検査でディレクトリ内へ限定する。拡張子は`.md`のみ許可する
- 1ファイル上限1MiB（超過書き込みはエラー）。読み取りは`io.LimitReader`で制限する
- 書き込みは`0o600`、ディレクトリ作成は`0o700`

追加するツール（internal/tool/memory.go）を次に示す。

| ツール | 引数 | 動作 |
|---|---|---|
| `memory_write` | `path`（相対）, `content`, `append`（bool、省略時false） | Store.Writeを呼ぶ。成功時は書き込み先とバイト数を返す |
| `memory_read` | `path`（相対、省略時はMEMORY.md） | Store.Readの内容を返す |

既存ツールと同じく`Spec()` / `Execute()`を実装し、引数検証エラーは`Result{IsError: true}`で返す。

### 6.3 REPLコマンド（internal/transport/cliui/repl.go）

- `/memory`: `Store.List`の一覧と`MEMORY.md`の内容を表示する。`/memory <file>`で指定トピックファイルを表示する。表示のみで編集はしない
- `# <本文>`: 入力をLLMへ送らず、`memories.md`へ`- <本文>`を追記し、`MEMORY.md`索引へも1行追記する。保存先パスを1行表示する
- どちらもメモリ機能が無効（enabled=falseまたはStore初期化失敗）のときはその旨を表示して何もしない

### 6.4 設定スキーマ（internal/config）

```yaml
agent:
  agents_md:
    enabled: true          # 既存
    max_bytes: 32768       # 既存: 1ファイル上限
    max_total_bytes: 32768 # 新規: 連結合計の上限
    global_dir: ~/.go-llm-agent   # 新規: グローバルAGENTS.mdの置き場所
  memory:
    enabled: true                     # 新規
    dir: ~/.go-llm-agent/projects     # 新規: プロジェクトキー配下にmemory/を作る
    index_max_lines: 200              # 新規
    index_max_bytes: 24576            # 新規
```

- 既定値は`applyDefaults`が適用し、`enabled`は既存AgentsMDConfigと同じ`*bool`方式で未指定とfalseを区別する
- `memory.enabled: false`のときはツール登録・索引注入・REPLコマンドのすべてを無効化し、ファイルシステムへアクセスしない
- Store初期化失敗時は既存notesと同じdegraded方針（エラーログを出して機能を無効化し、起動は継続する）

### 6.5 データフロー（起動時）

1. `resolveInstructions`（chat.go）: `instructions.Discover`でSource列を取得し、マーカー付きで`system_prompt`の後方へ連結する
2. `resolveMemory`（chat.go / deps.go）: プロジェクトキーを導出してStoreを作り、`ReadIndex`の内容をさらに後方へ注入する。`memory_write` / `memory_read`をツールregistryへ登録する
3. REPL起動時にStoreをOptions経由で渡し、`/memory`と`#`を有効化する

`agent run`と`agent serve`は現状AGENTS.mdを注入しないため、メモリ索引の注入もchat専用とする。両モードではツール登録のみ有効とし、REPLコマンドは対象外とする。

## 7. セキュリティ

- パス検証: すべての読み書きでLstat（シンボリックリンク拒否）、`EvalSymlinks`、prefix検査を行い、メモリディレクトリ / 探索ルートの外へのアクセスを遮断する
- サイズ上限: 読み取りは`io.LimitReader`、書き込みは事前サイズ検査。索引は行数とバイトの二重上限
- プロンプトインジェクション: 指示ファイルもメモリも信頼境界マーカーで包み、上位指示を上書きできないことを明記する。メモリはエージェント自身の出力由来であり、ツール出力経由の汚染がありうる前提で扱う
- 機密情報: メモリはリポジトリ外（`~/.go-llm-agent`）に置くためコミット混入は構造的に起きない。gitleaksの走査対象にも含まれない

## 8. テスト計画

- `internal/instructions`: 探索順序（グローバル→ルート→cwd）、1ディレクトリ1ファイル、合計上限打ち切り、importの深さ上限・循環・フェンス内スキップ・ルート外拒否、シンボリックリンク拒否。table-drivenで網羅する
- `internal/memory`（automemory）: 索引の二重上限、rune境界切り詰め、path traversal拒否（`../`、絶対パス、シンボリックリンク）、拡張子制限、append/overwrite、1MiB上限
- `internal/tool`: 引数検証エラー、正常系、Storeエラーの伝播
- `internal/transport/cliui`: `/memory`表示、`#`追記、無効時のメッセージ
- E2E: `tests/e2e/fixtures/memory_exercise`を新設し、stub LLMでmemory_write→プロセス再起動→索引がシステムプロンプトへ注入されることを検証する
- 品質ゲート: `make quality`全Pass、変更行のmutation全KILL、変更関数のCRAP15以下、変更コードのカバレッジ80%以上

## 9. 実装タスク（TDD）

1. `internal/instructions`: Discoverと連結（テスト→実装）
2. `internal/instructions`: importの展開（テスト→実装）
3. `internal/memory`: Storeとプロジェクトキー導出（テスト→実装）
4. `internal/tool`: memory_write / memory_read（テスト→実装）
5. `internal/config`: 設定追加と既定値（テスト→実装）
6. `cmd/agent`: 配線（resolveInstructions / resolveMemory、既存resolveAgentsMDの置き換え）
7. `internal/transport/cliui`: `/memory`と`#`（テスト→実装）
8. E2E fixture: memory_exercise
9. READMEとドキュメント更新
