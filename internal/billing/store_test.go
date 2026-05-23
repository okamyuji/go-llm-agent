package billing

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStore_AppendAndQuerySession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewFileStore(filepath.Join(dir, "billing.jsonl"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	for i := range 3 {
		_ = store.Append(ctx, Snapshot{
			SessionID:    "sess-a",
			Provider:     "openai",
			Model:        "gpt",
			InputTokens:  10 * (i + 1),
			OutputTokens: 5 * (i + 1),
			CostJPY:      float64(i + 1),
			At:           time.Date(2026, 5, 23, 12, 0, i, 0, time.UTC),
		})
	}
	_ = store.Append(ctx, Snapshot{
		SessionID: "sess-b",
		Provider:  "openai",
		Model:     "gpt",
		At:        time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC),
	})

	got, err := store.QuerySession(ctx, "sess-a")
	if err != nil {
		t.Fatalf("QuerySession: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len(QuerySession sess-a) = %d want 3", len(got))
	}
	for i, snap := range got {
		if snap.InputTokens != 10*(i+1) {
			t.Errorf("snap %d InputTokens = %d", i, snap.InputTokens)
		}
	}
}

func TestFileStore_QueryDate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewFileStore(filepath.Join(dir, "billing.jsonl"))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	_ = store.Append(ctx, Snapshot{SessionID: "a", At: time.Date(2026, 5, 23, 1, 0, 0, 0, time.UTC)})
	_ = store.Append(ctx, Snapshot{SessionID: "b", At: time.Date(2026, 5, 24, 1, 0, 0, 0, time.UTC)})
	_ = store.Append(ctx, Snapshot{SessionID: "c", At: time.Date(2026, 5, 23, 23, 59, 0, 0, time.UTC)})

	got, err := store.QueryDate(ctx, "2026-05-23")
	if err != nil {
		t.Fatalf("QueryDate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(QueryDate) = %d want 2", len(got))
	}
}

func TestFileStore_ParentDirAutoCreated(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "subdir", "billing.jsonl")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Append(context.Background(), Snapshot{SessionID: "x", At: time.Now().UTC()}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}
