package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

// ShellTool shell ツールの実装
type ShellTool struct {
	cfg    config.ShellToolConfig
	logger *slog.Logger
	allow  map[string]struct{}
}

// NewShell config と logger を受け取り ShellTool を生成する
func NewShell(cfg config.ShellToolConfig, logger *slog.Logger) *ShellTool {
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	if cfg.MaxTimeoutSeconds <= 0 {
		cfg.MaxTimeoutSeconds = 300
	}
	allow := map[string]struct{}{}
	for _, b := range cfg.AllowBinaries {
		allow[b] = struct{}{}
	}
	return &ShellTool{cfg: cfg, logger: logger, allow: allow}
}

// Spec ツール定義を返す
func (t *ShellTool) Spec() Spec {
	return Spec{
		Name:        "shell",
		Description: "短命のシェルコマンドを実行する。allow_binaries に含まれるコマンドのみ実行可能",
		Schema: json.RawMessage(`{
"type":"object",
"properties":{
"command":{"type":"string","description":"argv の先頭バイナリ名"},
"args":{"type":"array","items":{"type":"string"}},
"timeout_seconds":{"type":"integer","minimum":1}
},
"required":["command"]
}`),
	}
}

type shellArgs struct {
	Command        string   `json:"command"`
	Args           []string `json:"args"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// Execute コマンドを許可リスト照合してから実行する
func (t *ShellTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a shellArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if a.Command == "" {
		return Result{IsError: true, Content: "command is required"}, nil
	}
	base := filepath.Base(a.Command)
	if _, ok := t.allow[base]; !ok {
		return Result{IsError: true, Content: fmt.Sprintf("shell: %q は allow_binaries に含まれていません", base)}, nil
	}
	resolved, err := exec.LookPath(a.Command)
	if err != nil {
		return Result{IsError: true, Content: fmt.Sprintf("shell: %v", err)}, nil
	}
	if filepath.Base(resolved) != base {
		return Result{IsError: true, Content: fmt.Sprintf("shell: 解決後のバイナリ名が一致しません %s vs %s", filepath.Base(resolved), base)}, nil
	}

	timeout := a.TimeoutSeconds
	if timeout <= 0 {
		timeout = t.cfg.TimeoutSeconds
	}
	if timeout > t.cfg.MaxTimeoutSeconds {
		timeout = t.cfg.MaxTimeoutSeconds
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, resolved, a.Args...)
	out, runErr := cmd.CombinedOutput()
	t.logger.Info("shell exec",
		"command", base,
		"args", strings.Join(a.Args, " "),
		"timeout", timeout,
		"exit_err", runErr,
	)
	if runErr != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return Result{IsError: true, Content: fmt.Sprintf("shell timeout after %ds\n%s", timeout, string(out))}, nil
		}
		return Result{IsError: true, Content: fmt.Sprintf("shell: %v\n%s", runErr, string(out))}, nil
	}
	return Result{Content: string(out)}, nil
}
