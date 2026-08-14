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
	}, logger, nil)
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

func TestShell_ArgDeny_GitConfigGlobal(t *testing.T) {
	sh := newShell(t, []string{"git"})
	res, _ := sh.Execute(context.Background(), json.RawMessage(`{"command":"git","args":["config","--global","user.email","x@y"]}`))
	if !res.IsError {
		t.Fatal("git config --global は deny されるべき")
	}
	if !strings.Contains(res.Content, "deny") {
		t.Fatalf("deny 理由を含むこと: %q", res.Content)
	}
}

func TestShell_ArgDeny_BashExec(t *testing.T) {
	sh := newShell(t, []string{"bash"})
	res, _ := sh.Execute(context.Background(), json.RawMessage(`{"command":"bash","args":["-c","echo hi"]}`))
	if !res.IsError {
		t.Fatal("bash -c は deny されるべき")
	}
}

func TestShell_ArgDeny_GitSSHCommandInjection(t *testing.T) {
	sh := newShell(t, []string{"git"})
	res, _ := sh.Execute(context.Background(), json.RawMessage(`{"command":"git","args":["-c","core.sshCommand=evil","fetch"]}`))
	if !res.IsError {
		t.Fatal("git -c core.sshCommand 注入は deny されるべき")
	}
}

func TestShell_ArgDeny_GoEnvWrite(t *testing.T) {
	sh := newShell(t, []string{"go"})
	res, _ := sh.Execute(context.Background(), json.RawMessage(`{"command":"go","args":["env","-w","GOPROXY=evil"]}`))
	if !res.IsError {
		t.Fatal("go env -w は deny")
	}
}

func TestShell_AuditLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sh := tool.NewShell(config.ShellToolConfig{TimeoutSeconds: 5, MaxTimeoutSeconds: 5, AllowBinaries: []string{"echo"}}, logger, nil)
	_, _ = sh.Execute(context.Background(), json.RawMessage(`{"command":"echo","args":["audit-test"]}`))
	if !strings.Contains(buf.String(), `tool=shell`) {
		t.Fatalf("audit ログに tool=shell が含まれること: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "binary=echo") {
		t.Fatalf("audit ログに binary=echo: %s", buf.String())
	}
}
