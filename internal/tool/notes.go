package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/okamyuji/go-llm-agent/internal/memory"
)

// NoteAddArgs note_add ツールの引数
type NoteAddArgs struct {
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags"`
}

// NoteSearchArgs note_search ツールの引数
type NoteSearchArgs struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

// noteAddSchema note_add の JSON Schema
var noteAddSchema = json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"body":{"type":"string"},"tags":{"type":"array","items":{"type":"string"}}},"required":["title","body"]}`)

// noteSearchSchema note_search の JSON Schema
var noteSearchSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"top_k":{"type":"integer"}},"required":["query"]}`)

// NoteAddTool ノートを 1 件追加するツール
type NoteAddTool struct{ Store memory.NoteStore }

// Spec ツール仕様を返す
func (t *NoteAddTool) Spec() Spec {
	return Spec{Name: "note_add", Description: "ローカルノートに 1 件追加する", Schema: noteAddSchema}
}

// Execute note を Store.Add で追加する
func (t *NoteAddTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a NoteAddArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: fmt.Sprintf("invalid args: %v", err)}, nil
	}
	if a.Title == "" || a.Body == "" {
		return Result{IsError: true, Content: "title and body are required"}, nil
	}
	n, err := t.Store.Add(ctx, memory.Note{Title: a.Title, Body: a.Body, Tags: a.Tags})
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	return Result{Content: fmt.Sprintf("note added id=%s title=%q", n.ID, n.Title)}, nil
}

// NoteSearchTool ノートを全文検索するツール
type NoteSearchTool struct{ Store memory.NoteStore }

// Spec ツール仕様を返す
func (t *NoteSearchTool) Spec() Spec {
	return Spec{Name: "note_search", Description: "ローカルノートを全文検索する", Schema: noteSearchSchema}
}

// Execute Store.Search で上位 K 件を返す
func (t *NoteSearchTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a NoteSearchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: fmt.Sprintf("invalid args: %v", err)}, nil
	}
	if a.Query == "" {
		return Result{IsError: true, Content: "query is required"}, nil
	}
	// 負値は入力エラーとして扱い、0 のときのみ既定値 5 にフォールバックする
	// 過大な top_k は Store 側のメモリ使用量を肥大化させるため maxToolTopK で上限する
	const maxToolTopK = 100
	if a.TopK < 0 {
		return Result{IsError: true, Content: "top_k must be non-negative"}, nil
	}
	if a.TopK > maxToolTopK {
		return Result{IsError: true, Content: fmt.Sprintf("top_k must be <= %d", maxToolTopK)}, nil
	}
	topK := a.TopK
	if topK == 0 {
		topK = 5
	}
	notes, err := t.Store.Search(ctx, a.Query, topK)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if len(notes) == 0 {
		return Result{Content: "no notes matched"}, nil
	}
	b, err := json.MarshalIndent(notes, "", "  ")
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	return Result{Content: string(b)}, nil
}
