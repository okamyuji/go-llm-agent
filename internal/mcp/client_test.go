package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func buildEchoServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "echo")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "../../tests/e2e/fixtures/mcp_echo_server")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build echo server: %v\n%s", err, out)
	}
	return bin
}

func TestClient_ListAndCall(t *testing.T) {
	bin := buildEchoServer(t)
	ctx := context.Background()
	c, err := NewStdioClient(ctx, []string{bin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("expected echo tool, got %+v", tools)
	}

	res, err := c.Call(ctx, "echo", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("isError true: %s", res.Content)
	}
	if want := `echo: {"msg":"hi"}`; res.Content != want {
		t.Errorf("content = %q want %q", res.Content, want)
	}
}

func TestClient_UnknownMethodReturnsError(t *testing.T) {
	bin := buildEchoServer(t)
	ctx := context.Background()
	c, err := NewStdioClient(ctx, []string{bin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.call("nonexistent/method", nil); err == nil {
		t.Fatal("expected error from unknown method")
	}
}

func TestNewStdioClient_EmptyCommandErrors(t *testing.T) {
	t.Parallel()
	if _, err := NewStdioClient(context.Background(), nil); err == nil {
		t.Fatal("expected error for empty command")
	}
}
