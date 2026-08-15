package tool_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/memory"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func newTestNoteStore(t *testing.T) memory.NoteStore {
	t.Helper()
	dir := t.TempDir()
	s, err := memory.NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
	if err != nil {
		t.Fatalf("NewFileNoteStore: %v", err)
	}
	return s
}

func TestNoteAddTool_Spec(t *testing.T) {
	nt := &tool.NoteAddTool{Store: newTestNoteStore(t)}
	spec := nt.Spec()
	if spec.Name != "note_add" {
		t.Fatalf("Name=%q 期待=note_add", spec.Name)
	}
	if spec.Schema == nil {
		t.Fatal("Schema が nil")
	}
}

func TestNoteAddTool_Execute(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"正常系", `{"title":"t1","body":"b1","tags":["a"]}`, false},
		{"title欠落", `{"body":"b1"}`, true},
		{"body欠落", `{"title":"t1"}`, true},
		{"不正JSON", `{`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nt := &tool.NoteAddTool{Store: newTestNoteStore(t)}
			res, err := nt.Execute(context.Background(), json.RawMessage(tt.raw))
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if res.IsError != tt.wantErr {
				t.Fatalf("IsError=%v 期待=%v content=%q", res.IsError, tt.wantErr, res.Content)
			}
		})
	}
}

func TestNoteAddTool_Execute_StoreError(t *testing.T) {
	// notes ファイルパスをディレクトリにすることで OpenFile(O_WRONLY) を失敗させる
	dir := t.TempDir()
	notesPath := filepath.Join(dir, "notes.jsonl")
	if err := os.Mkdir(notesPath, 0o700); err != nil {
		t.Fatalf("os.Mkdir: %v", err)
	}
	s, err := memory.NewFileNoteStore(notesPath)
	if err != nil {
		t.Fatalf("NewFileNoteStore: %v", err)
	}
	nt := &tool.NoteAddTool{Store: s}
	res, err := nt.Execute(context.Background(), json.RawMessage(`{"title":"t","body":"b"}`))
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("Store エラー時は IsError=true 期待, content=%q", res.Content)
	}
}

func TestNoteSearchTool_Spec(t *testing.T) {
	nt := &tool.NoteSearchTool{Store: newTestNoteStore(t)}
	spec := nt.Spec()
	if spec.Name != "note_search" {
		t.Fatalf("Name=%q 期待=note_search", spec.Name)
	}
	if spec.Schema == nil {
		t.Fatal("Schema が nil")
	}
}

func TestNoteSearchTool_Execute(t *testing.T) {
	store := newTestNoteStore(t)
	addTool := &tool.NoteAddTool{Store: store}
	if _, err := addTool.Execute(context.Background(), json.RawMessage(`{"title":"golang tips","body":"defer は最後に実行される"}`)); err != nil {
		t.Fatalf("seed add error: %v", err)
	}

	searchTool := &tool.NoteSearchTool{Store: store}

	t.Run("正常系ヒット", func(t *testing.T) {
		res, err := searchTool.Execute(context.Background(), json.RawMessage(`{"query":"golang"}`))
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if res.IsError {
			t.Fatalf("err: %s", res.Content)
		}
		if !strings.Contains(res.Content, "golang tips") {
			t.Fatalf("content=%q", res.Content)
		}
	})

	t.Run("ヒットなし", func(t *testing.T) {
		res, err := searchTool.Execute(context.Background(), json.RawMessage(`{"query":"存在しない語句xyz"}`))
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if res.IsError {
			t.Fatalf("err: %s", res.Content)
		}
		if res.Content != "no notes matched" {
			t.Fatalf("content=%q", res.Content)
		}
	})

	t.Run("query欠落", func(t *testing.T) {
		res, err := searchTool.Execute(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !res.IsError {
			t.Fatal("query 欠落時は IsError=true 期待")
		}
	})

	t.Run("top_k負値", func(t *testing.T) {
		res, err := searchTool.Execute(context.Background(), json.RawMessage(`{"query":"golang","top_k":-1}`))
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !res.IsError {
			t.Fatal("top_k 負値は IsError=true 期待")
		}
	})

	t.Run("top_k上限超過", func(t *testing.T) {
		res, err := searchTool.Execute(context.Background(), json.RawMessage(`{"query":"golang","top_k":101}`))
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !res.IsError {
			t.Fatal("top_k > 100 は IsError=true 期待")
		}
	})

	t.Run("不正JSON", func(t *testing.T) {
		res, err := searchTool.Execute(context.Background(), json.RawMessage(`{`))
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !res.IsError {
			t.Fatal("不正 JSON は IsError=true 期待")
		}
	})
}
