package cliui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// sessionEntry セッション JSONL の 1 行。llm.Message を往復できるだけの
// フィールドを持つ。tool_calls を持つ assistant メッセージと、
// tool_call_id/name を持つ tool メッセージの両方を再現できるようにする。
type sessionEntry struct {
	Ts         time.Time      `json:"ts"`
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
}

func messageToEntry(m llm.Message) sessionEntry {
	return sessionEntry{
		Ts:         time.Now().UTC(),
		Role:       string(m.Role),
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
		ToolCalls:  m.ToolCalls,
	}
}

// validRoles entryToMessage が受理する role の集合
var validRoles = map[llm.Role]bool{
	llm.RoleSystem:    true,
	llm.RoleUser:      true,
	llm.RoleAssistant: true,
	llm.RoleTool:      true,
}

// entryToMessage e を llm.Message へ変換する。role が未知の場合はエラーを返す
// (呼び出し元がその行を読み飛ばして警告する)
func entryToMessage(e sessionEntry) (llm.Message, error) {
	role := llm.Role(e.Role)
	if !validRoles[role] {
		return llm.Message{}, fmt.Errorf("session: unknown role %q", e.Role)
	}
	return llm.Message{
		Role:       role,
		Content:    e.Content,
		ToolCallID: e.ToolCallID,
		Name:       e.Name,
		ToolCalls:  e.ToolCalls,
	}, nil
}

// sessionWriter 1 セッション分の JSONL への追記を担う。/clear で発行される
// rotate によって、以後の追記先ファイルを新しい ID へ差し替えられる。
// REPL は単一 goroutine から呼ぶが、将来の並行化に備えて mutex で保護する。
type sessionWriter struct {
	dir string
	mu  sync.Mutex
	id  string
}

func newSessionWriter(dir, id string) *sessionWriter {
	return &sessionWriter{dir: dir, id: id}
}

// sessionID 現在のアクティブなセッション ID を返す
func (w *sessionWriter) sessionID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.id
}

func (w *sessionWriter) path() string {
	return filepath.Join(w.dir, w.id+".jsonl")
}

// append m を 1 行として現在のアクティブファイルへ追記する
func (w *sessionWriter) append(m llm.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("session mkdir: %w", err)
	}
	b, err := json.Marshal(messageToEntry(m))
	if err != nil {
		return fmt.Errorf("session marshal: %w", err)
	}
	f, err := os.OpenFile(w.path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("session open: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("session write: %w", err)
	}
	return nil
}

// rotate 以後の追記先を新しいセッション ID へ切り替える。既存ファイルは
// 変更しない。新しい ID を返す
func (w *sessionWriter) rotate() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.id = newSessionID(w.dir)
	return w.id
}

// maxSessionIDAttempts newSessionID が連番サフィックスを試す最大回数。
// 同一秒に 1000 セッションが開始されることは運用上ありえないため、この回数を
// 使い切った場合はディレクトリ側の異常とみなしフォールバック ID を返す
const maxSessionIDAttempts = 1000

// newSessionID dir 内で衝突しない、時系列順と辞書順が一致するファイル名安全な
// ID を UTC タイムスタンプ（秒精度）から生成する。同一秒内での衝突は
// 連番サフィックスで避ける。
//
// os.Stat が NotExist 以外のエラー（EACCES・ENOTDIR 等）を返す場合、衝突の
// 有無を判定できない。この場合はナノ秒サフィックス付きのフォールバック ID を
// 即座に返す。os.IsNotExist(err) だけをループの終了条件にすると、権限エラーの
// ディレクトリでは終了条件が永久に偽となり無限ループになるためである。
// 試行回数にも上限を設け、いずれの経路でも有限時間で返る。
func newSessionID(dir string) string {
	base := time.Now().UTC().Format("20060102T150405Z")
	fallback := fmt.Sprintf("%s-%09d", base, time.Now().UTC().Nanosecond())
	id := base
	for i := 2; i < maxSessionIDAttempts+2; i++ {
		_, err := os.Stat(filepath.Join(dir, id+".jsonl"))
		if os.IsNotExist(err) {
			return id
		}
		if err != nil {
			// NotExist 以外のエラー。衝突判定ができないため打ち切る
			return fallback
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return fallback
}

// latestSessionID dir 内の *.jsonl のうち辞書順で最大の ID（= newSessionID の
// 形式では最新のタイムスタンプ）を返す。dir が存在しない、または対象ファイルが
// 1 件もない場合は ok=false を返す（エラーではない。新規セッションとして
// 開始するのが正しい振る舞いのため）。
func latestSessionID(dir string) (id string, ok bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("session list: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	if len(ids) == 0 {
		return "", false, nil
	}
	sort.Strings(ids)
	return ids[len(ids)-1], true, nil
}

// loadSession dir/id.jsonl を読み llm.Message 列へ変換する。壊れた行
// （不正な JSON、未知の role）は標準エラーへ警告を出して読み飛ばす。
func loadSession(dir, id string, warn func(string)) ([]llm.Message, error) {
	f, err := os.Open(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		return nil, fmt.Errorf("session open: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var out []llm.Message
	line := 0
	for sc.Scan() {
		line++
		var e sessionEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			warn(fmt.Sprintf("[resume] %s:%d 行を読み飛ばしました: %v", id, line, err))
			continue
		}
		m, err := entryToMessage(e)
		if err != nil {
			warn(fmt.Sprintf("[resume] %s:%d 行を読み飛ばしました: %v", id, line, err))
			continue
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("session scan: %w", err)
	}
	return out, nil
}

// ChatSessionsDir chat セッション JSONL の保存先を解決する。
// chatDir が空文字なら <sessionsDir>/chat を使う。
// 引数は呼び出し側で展開済み (~ 等の解決済み) の絶対パスを想定する
func ChatSessionsDir(chatDir, sessionsDir string) string {
	if chatDir != "" {
		return chatDir
	}
	return filepath.Join(sessionsDir, "chat")
}

// ResumeLatestSession resume が true のとき dir 内の最新セッションを復元し、
// その ID とメッセージ列を返す。復元対象が無い場合と resume が false の場合は
// 空値を返す (エラーではない)。notify は利用者向けメッセージを受け取る
// コールバックで、warn は壊れた行の警告を受け取る。
// cmdChat と E2E fixture の双方がこの関数を呼び、-resume の解釈を共有する
func ResumeLatestSession(dir string, resume bool, notify, warn func(string)) (string, []llm.Message, error) {
	if !resume {
		return "", nil, nil
	}
	latest, ok, lerr := latestSessionID(dir)
	if lerr != nil {
		return "", nil, fmt.Errorf("resume: %w", lerr)
	}
	if !ok {
		notify("[resume] 復元可能なセッションが見つかりません。新規セッションを開始します")
		return "", nil, nil
	}
	hist, herr := loadSession(dir, latest, warn)
	if herr != nil {
		return "", nil, fmt.Errorf("resume: %w", herr)
	}
	notify(fmt.Sprintf("[resume] %s から %d 件のメッセージを復元しました", latest, len(hist)))
	return latest, hist, nil
}

// LatestSessionID dir 内の最新セッション ID を返す公開ラッパー
func LatestSessionID(dir string) (string, bool, error) { return latestSessionID(dir) }

// LoadSession dir/id.jsonl を読み込み llm.Message 列へ変換する公開ラッパー。
// warn は壊れた行の警告メッセージを受け取るコールバック
func LoadSession(dir, id string, warn func(string)) ([]llm.Message, error) {
	return loadSession(dir, id, warn)
}
