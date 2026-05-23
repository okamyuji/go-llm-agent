package memory

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFileNoteStore_AddAndSearch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Add(ctx, Note{Title: "Go 並列処理", Body: "errgroup と semaphore の使い方", Tags: []string{"go", "concurrency"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(ctx, Note{Title: "Python 入門", Body: "list comprehension", Tags: []string{"python"}}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Search(ctx, "go errgroup", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if got[0].Title != "Go 並列処理" {
		t.Errorf("expected Go note at top, got %q", got[0].Title)
	}
}

func TestFileNoteStore_TopKLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
	if err != nil {
		t.Fatalf("NewFileNoteStore: %v", err)
	}
	ctx := context.Background()
	for range 5 {
		if _, err := s.Add(ctx, Note{Title: "note", Body: "test"}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	got, err := s.Search(ctx, "note", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("topK=2 expected, got %d", len(got))
	}
}

func TestFileNoteStore_EmptyQueryReturnsNil(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
	if err != nil {
		t.Fatalf("NewFileNoteStore: %v", err)
	}
	got, err := s.Search(context.Background(), "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("empty query must yield nil, got %v", got)
	}
}

func TestFileNoteStore_MissingFileReturnsNil(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
	if err != nil {
		t.Fatalf("NewFileNoteStore: %v", err)
	}
	got, err := s.Search(context.Background(), "hello", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("missing file must yield nil, got %v", got)
	}
}

func TestScoreNote_WeightOrder(t *testing.T) {
	t.Parallel()
	n := Note{Title: "alpha", Body: "beta gamma", Tags: []string{"alpha"}}
	if s := scoreNote(n, []string{"alpha"}); s != 5 {
		t.Errorf("title+tag for 'alpha' must be 3+2=5, got %d", s)
	}
}

func TestTokenize_RemovesPunctuation(t *testing.T) {
	t.Parallel()
	got := tokenize("Hello, World! How are you?")
	want := []string{"hello", "world", "how", "are", "you"}
	if len(got) != len(want) {
		t.Fatalf("tokens: %v want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("token[%d] = %q want %q", i, got[i], w)
		}
	}
}

// TestTokenize_JapaneseBigram 日本語クエリが rune 2-gram に展開されることを検証する
// 分かち書きしない言語でも本文との部分一致でヒットすることを担保する
func TestTokenize_JapaneseBigram(t *testing.T) {
	t.Parallel()
	got := tokenize("メモリ管理")
	want := []string{"メモ", "モリ", "リ管", "管理"}
	if len(got) != len(want) {
		t.Fatalf("tokens: %v want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("token[%d] = %q want %q", i, got[i], w)
		}
	}
}

// TestTokenize_SingleRuneNonASCII 1 文字のみの非 ASCII トークンが
// 2-gram 展開できずに脱落しないこと (そのまま 1 トークンとして残る) を確認する
func TestTokenize_SingleRuneNonASCII(t *testing.T) {
	t.Parallel()
	got := tokenize("猫")
	if len(got) != 1 || got[0] != "猫" {
		t.Fatalf("single-rune token must survive: %v", got)
	}
}

// TestFileNoteStore_JapaneseSearch 日本語の Title/Body を含むノートが
// 日本語クエリで検索ヒットすることを検証する (回帰防止)
func TestFileNoteStore_JapaneseSearch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
	if err != nil {
		t.Fatalf("NewFileNoteStore: %v", err)
	}
	ctx := context.Background()
	if _, err := s.Add(ctx, Note{Title: "Go の メモリ管理", Body: "GC とエスケープ解析の話", Tags: []string{"go"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := s.Search(ctx, "メモリ管理", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least 1 result for Japanese query")
	}
}
