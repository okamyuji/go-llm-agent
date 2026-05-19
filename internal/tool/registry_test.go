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
