package cliui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileHistoryLoadsExistingRlwrapFile(t *testing.T) {
	// rlwrap -H が書いた 1 行 1 エントリの履歴ファイルをそのまま引き継ぐ
	path := filepath.Join(t.TempDir(), "hist")
	if err := os.WriteFile(path, []byte("古い質問\n新しい質問\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newFileHistory(path, 100)
	if h.Len() != 2 {
		t.Fatalf("Len=%d, want 2", h.Len())
	}
	if h.At(0) != "新しい質問" || h.At(1) != "古い質問" {
		t.Errorf("At(0)=%q At(1)=%q, want most-recent first", h.At(0), h.At(1))
	}
}

func TestFileHistoryAddPersistsAndDedupes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hist")
	h := newFileHistory(path, 100)
	h.Add("q1")
	h.Add("q1") // 直前と同じエントリは重複保存しない
	h.Add("")   // 空行は保存しない
	h.Add("q2")
	if h.Len() != 2 {
		t.Fatalf("Len=%d, want 2 (dedup + skip empty)", h.Len())
	}
	reloaded := newFileHistory(path, 100)
	if reloaded.Len() != 2 || reloaded.At(0) != "q2" || reloaded.At(1) != "q1" {
		t.Errorf("reloaded Len=%d At(0)=%q At(1)=%q, want persisted q2,q1",
			reloaded.Len(), reloaded.At(0), reloaded.At(1))
	}
}

func TestFileHistoryBoundsEntries(t *testing.T) {
	h := newFileHistory("", 3)
	for _, e := range []string{"a", "b", "c", "d"} {
		h.Add(e)
	}
	if h.Len() != 3 || h.At(0) != "d" || h.At(2) != "b" {
		t.Errorf("Len=%d At(0)=%q At(2)=%q, want bounded to last 3", h.Len(), h.At(0), h.At(2))
	}
}

func TestFileHistoryEmptyPathIsMemoryOnly(t *testing.T) {
	h := newFileHistory("", 10)
	h.Add("q")
	if h.Len() != 1 || h.At(0) != "q" {
		t.Errorf("memory-only history broken: Len=%d", h.Len())
	}
}
