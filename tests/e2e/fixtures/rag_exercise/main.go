// Package main 11 設計書の NoteStore と note_add/note_search ツールの動作確認
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/okamyuji/go-llm-agent/internal/memory"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func main() {
	dir, err := os.MkdirTemp("", "rag-fixture-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ns, err := memory.NewFileNoteStore(filepath.Join(dir, "notes.jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		os.Exit(2)
	}
	add := &tool.NoteAddTool{Store: ns}
	search := &tool.NoteSearchTool{Store: ns}

	if r, _ := add.Execute(context.Background(), json.RawMessage(`{"title":"OTel","body":"distributed tracing setup","tags":["obs"]}`)); r.IsError {
		fmt.Fprintln(os.Stderr, "add fail:", r.Content)
		os.Exit(3)
	}
	if r, _ := add.Execute(context.Background(), json.RawMessage(`{"title":"Auth","body":"bearer token guide","tags":["security"]}`)); r.IsError {
		fmt.Fprintln(os.Stderr, "add fail 2:", r.Content)
		os.Exit(4)
	}

	r, _ := search.Execute(context.Background(), json.RawMessage(`{"query":"OTel tracing","top_k":3}`))
	if r.IsError {
		fmt.Fprintln(os.Stderr, "search fail:", r.Content)
		os.Exit(5)
	}
	fmt.Printf("search_top=%t\n", contains(r.Content, "OTel"))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
