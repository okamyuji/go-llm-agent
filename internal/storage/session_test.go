package storage_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/storage"
)

func TestSessionStore_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSessionStore(dir)
	ctx := context.Background()

	if err := store.Append(ctx, "sess1", storage.Entry{Role: "user", Content: "hi"}); err != nil {
		t.Fatalf("append1: %v", err)
	}
	if err := store.Append(ctx, "sess1", storage.Entry{Role: "assistant", Content: "hello"}); err != nil {
		t.Fatalf("append2: %v", err)
	}
	entries, err := store.Read(ctx, "sess1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries len=%d", len(entries))
	}
	if entries[0].Content != "hi" || entries[1].Content != "hello" {
		t.Fatalf("contents %v", entries)
	}
	path := filepath.Join(dir, "sess1.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("jsonl ファイルが生成されること: %v", err)
	}
}
