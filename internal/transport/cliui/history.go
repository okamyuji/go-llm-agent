package cliui

import (
	"os"
	"strings"
)

// historyMaxEntries メモリ上に保持する履歴エントリ数の上限
const historyMaxEntries = 1000

// fileHistory は term.History 実装。1 行 1 エントリのテキストファイルへ永続化し、
// rlwrap -H が使う ~/.agent_history 形式をそのまま引き継げる。
// path が空ならセッション内のみ (テスト・履歴無効化用)。
type fileHistory struct {
	path    string
	entries []string // 古い → 新しい
	max     int
}

func newFileHistory(path string, max int) *fileHistory {
	h := &fileHistory{path: path, max: max}
	if path == "" {
		return h
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// ファイルが無い・読めない場合は空履歴で開始する (初回起動)
		return h
	}
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		h.append(line)
	}
	return h
}

func (h *fileHistory) append(entry string) {
	h.entries = append(h.entries, entry)
	if len(h.entries) > h.max {
		h.entries = h.entries[len(h.entries)-h.max:]
	}
}

// Add は term.Terminal から確定行ごとに呼ばれる。空行と直前との重複は保存しない。
func (h *fileHistory) Add(entry string) {
	if strings.TrimSpace(entry) == "" {
		return
	}
	if len(h.entries) > 0 && h.entries[len(h.entries)-1] == entry {
		return
	}
	h.append(entry)
	if h.path == "" {
		return
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return // 履歴保存の失敗で REPL を止めない
	}
	_, _ = f.WriteString(entry + "\n")
	_ = f.Close()
}

func (h *fileHistory) Len() int { return len(h.entries) }

// At は 0 を最新として履歴を返す (term.History 仕様)。
func (h *fileHistory) At(idx int) string { return h.entries[len(h.entries)-1-idx] }
