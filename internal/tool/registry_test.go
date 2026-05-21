package tool_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

type fakeTool struct{ name string }

func (f *fakeTool) Spec() tool.Spec {
	return tool.Spec{Name: f.name, Description: "x", Schema: json.RawMessage(`{}`)}
}
func (f *fakeTool) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: "ok"}, nil
}

func TestRegistry_LookupAndList(t *testing.T) {
	r := tool.NewRegistry([]tool.Tool{&fakeTool{name: "a"}, &fakeTool{name: "b"}}, []string{"a"})
	if _, ok := r.Lookup("a"); !ok {
		t.Fatal("a 有効")
	}
	if _, ok := r.Lookup("b"); ok {
		t.Fatal("b は EnabledTools 外なので無効")
	}
	if len(r.List()) != 1 {
		t.Fatalf("list len=%d", len(r.List()))
	}
}

func TestRegistry_UnknownLookup(t *testing.T) {
	r := tool.NewRegistry([]tool.Tool{}, nil)
	if _, ok := r.Lookup("z"); ok {
		t.Fatal("未登録は false")
	}
}

func TestRegistry_DefaultsReadonly_WhenNil(t *testing.T) {
	r := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "fs_read"},
		&fakeTool{name: "fs_write"},
		&fakeTool{name: "shell"},
		&fakeTool{name: "http_fetch"},
		&fakeTool{name: "search_files"},
	}, nil)
	if _, ok := r.Lookup("fs_write"); ok {
		t.Fatal("enabled_tools 未指定時に fs_write は無効でなければならない")
	}
	if _, ok := r.Lookup("shell"); ok {
		t.Fatal("enabled_tools 未指定時に shell は無効でなければならない")
	}
	if _, ok := r.Lookup("fs_read"); !ok {
		t.Fatal("fs_read は readonly セットに含まれるべき")
	}
}

func TestRegistry_DefaultsReadonly_WhenEmptySlice(t *testing.T) {
	r := tool.NewRegistry([]tool.Tool{
		&fakeTool{name: "fs_read"},
		&fakeTool{name: "fs_write"},
	}, []string{})
	if _, ok := r.Lookup("fs_write"); ok {
		t.Fatal("空配列でも readonly デフォルトを適用")
	}
}

func TestSandbox_AllowsConfiguredPath(t *testing.T) {
	root := t.TempDir()
	sb := tool.NewSandbox([]string{root})
	if err := sb.CheckPath(filepath.Join(root, "sub", "file.txt")); err != nil {
		t.Fatalf("配下を許可: %v", err)
	}
}

func TestSandbox_RejectsOutside(t *testing.T) {
	root := t.TempDir()
	sb := tool.NewSandbox([]string{root})
	if err := sb.CheckPath("/etc/passwd"); err == nil {
		t.Fatal("外側はエラー")
	}
}
