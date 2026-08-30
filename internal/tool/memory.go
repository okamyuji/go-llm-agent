package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/okamyuji/go-llm-agent/internal/memory"
)

// MemoryWriteArgs memory_write ツールの引数
type MemoryWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Append  bool   `json:"append"`
}

// MemoryReadArgs memory_read ツールの引数
type MemoryReadArgs struct {
	Path string `json:"path"`
}

// memoryWriteSchema memory_write の JSON Schema
var memoryWriteSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"メモリディレクトリ直下の .md ファイル名"},"content":{"type":"string"},"append":{"type":"boolean","description":"true で追記、省略時は上書き"}},"required":["path","content"]}`)

// memoryReadSchema memory_read の JSON Schema
var memoryReadSchema = json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"省略時は MEMORY.md"}},"required":[]}`)

// memoryReadMaxBytes memory_read で返す内容の上限
const memoryReadMaxBytes = 1 << 20

// MemoryWriteTool 自動メモリへ 1 ファイル書き込むツール
type MemoryWriteTool struct{ Store *memory.Store }

// Spec ツール仕様を返す
func (t *MemoryWriteTool) Spec() Spec {
	return Spec{
		Name:        "memory_write",
		Description: "自動メモリ (プロジェクト単位の永続メモ) へ .md ファイルを書き込む。索引 MEMORY.md も自分で更新する",
		Schema:      memoryWriteSchema,
	}
}

// Execute Store.Write で書き込む
func (t *MemoryWriteTool) Execute(_ context.Context, raw json.RawMessage) (Result, error) {
	var a MemoryWriteArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: fmt.Sprintf("invalid args: %v", err)}, nil
	}
	if a.Path == "" || a.Content == "" {
		return Result{IsError: true, Content: "path and content are required"}, nil
	}
	if err := t.Store.Write(a.Path, a.Content, a.Append); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	return Result{Content: fmt.Sprintf("memory written path=%s bytes=%d append=%t", a.Path, len(a.Content), a.Append)}, nil
}

// MemoryReadTool 自動メモリから 1 ファイル読み取るツール
type MemoryReadTool struct{ Store *memory.Store }

// Spec ツール仕様を返す
func (t *MemoryReadTool) Spec() Spec {
	return Spec{
		Name:        "memory_read",
		Description: "自動メモリの .md ファイルを読む。path 省略時は索引 MEMORY.md を返す",
		Schema:      memoryReadSchema,
	}
}

// Execute Store.Read で読み取る
func (t *MemoryReadTool) Execute(_ context.Context, raw json.RawMessage) (Result, error) {
	var a MemoryReadArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: fmt.Sprintf("invalid args: %v", err)}, nil
	}
	path := a.Path
	if path == "" {
		path = memory.IndexFileName
	}
	content, err := t.Store.Read(path, memoryReadMaxBytes)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	return Result{Content: content}, nil
}
