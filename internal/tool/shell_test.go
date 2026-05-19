package tool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func newShell(t *testing.T, allow []string) *tool.ShellTool {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	return tool.NewShell(config.ShellToolConfig{
		TimeoutSeconds: 5, MaxTimeoutSeconds: 5,
		AllowBinaries: allow,
	}, logger)
}

func TestShell_AllowedCommandRuns(t *testing.T) {
	sh := newShell(t, []string{"echo"})
	res, _ := sh.Execute(context.Background(), json.RawMessage(`{"command":"echo","args":["hello"]}`))
	if res.IsError {
		t.Fatalf("err: %s", res.Content)
	}
	if !strings.Contains(res.Content, "hello") {
		t.Fatalf("content=%q", res.Content)
	}
}

func TestShell_DeniedBinary(t *testing.T) {
	sh := newShell(t, []string{"echo"})
	res, _ := sh.Execute(context.Background(), json.RawMessage(`{"command":"rm","args":["-rf","/"]}`))
	if !res.IsError {
		t.Fatal("denied")
	}
	if !strings.Contains(res.Content, "allow_binaries") {
		t.Fatalf("err msg=%q", res.Content)
	}
}

func TestShell_Timeout(t *testing.T) {
	sh := newShell(t, []string{"sleep"})
	res, _ := sh.Execute(context.Background(), json.RawMessage(`{"command":"sleep","args":["5"],"timeout_seconds":1}`))
	if !res.IsError {
		t.Fatal("timeout 後は IsError")
	}
}
