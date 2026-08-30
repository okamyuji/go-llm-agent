package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/memory"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// newMemoryStore テスト用の自動メモリ Store を作るヘルパ
func newMemoryStore(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestMemoryWriteTool_Spec(t *testing.T) {
	mw := &tool.MemoryWriteTool{Store: newMemoryStore(t)}
	if mw.Spec().Name != "memory_write" {
		t.Fatalf("name=%q", mw.Spec().Name)
	}
	mr := &tool.MemoryReadTool{Store: newMemoryStore(t)}
	if mr.Spec().Name != "memory_read" {
		t.Fatalf("name=%q", mr.Spec().Name)
	}
}

func TestMemoryWriteTool_Execute(t *testing.T) {
	st := newMemoryStore(t)
	mw := &tool.MemoryWriteTool{Store: st}
	ctx := context.Background()

	cases := []struct {
		name    string
		args    string
		isError bool
	}{
		{"正常write", `{"path":"topic.md","content":"hello"}`, false},
		{"append", `{"path":"topic.md","content":" more","append":true}`, false},
		{"JSON壊れ", `{`, true},
		{"path欠落", `{"content":"x"}`, true},
		{"content欠落", `{"path":"a.md"}`, true},
		{"traversal", `{"path":"../x.md","content":"x"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := mw.Execute(ctx, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("Execute err: %v", err)
			}
			if res.IsError != tc.isError {
				t.Fatalf("IsError=%v, want %v (%s)", res.IsError, tc.isError, res.Content)
			}
		})
	}

	got, err := st.Read("topic.md", 1<<20)
	if err != nil || got != "hello more" {
		t.Fatalf("書き込み結果 %q, err=%v", got, err)
	}
}

func TestMemoryReadTool_Execute(t *testing.T) {
	st := newMemoryStore(t)
	if err := st.Write("MEMORY.md", "index body", false); err != nil {
		t.Fatalf("prep: %v", err)
	}
	if err := st.Write("topic.md", "topic body", false); err != nil {
		t.Fatalf("prep: %v", err)
	}
	mr := &tool.MemoryReadTool{Store: st}
	ctx := context.Background()

	res, err := mr.Execute(ctx, json.RawMessage(`{}`))
	if err != nil || res.IsError {
		t.Fatalf("path 省略: %+v err=%v", res, err)
	}
	if !strings.Contains(res.Content, "index body") {
		t.Fatalf("省略時に MEMORY.md が読まれていない: %q", res.Content)
	}

	res, err = mr.Execute(ctx, json.RawMessage(`{"path":"topic.md"}`))
	if err != nil || res.IsError || !strings.Contains(res.Content, "topic body") {
		t.Fatalf("topic 読み取り失敗: %+v err=%v", res, err)
	}

	res, err = mr.Execute(ctx, json.RawMessage(`{"path":"missing.md"}`))
	if err != nil || !res.IsError {
		t.Fatalf("不存在ファイルが IsError にならない: %+v", res)
	}

	res, err = mr.Execute(ctx, json.RawMessage(`{`))
	if err != nil || !res.IsError {
		t.Fatalf("JSON 壊れが IsError にならない: %+v", res)
	}
}
