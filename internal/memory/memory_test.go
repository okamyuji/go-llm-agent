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
	s, _ := NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
	ctx := context.Background()
	for i := range 5 {
		_, _ = s.Add(ctx, Note{Title: "note", Body: "test"})
		_ = i
	}
	got, _ := s.Search(ctx, "note", 2)
	if len(got) != 2 {
		t.Errorf("topK=2 expected, got %d", len(got))
	}
}

func TestFileNoteStore_EmptyQueryReturnsNil(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, _ := NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
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
	s, _ := NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
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
