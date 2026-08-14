// Package main 08-os-sandbox.md の darwin sandbox-exec 組込みを検証するフィクスチャ。
// internal/tool.NewShell を os_sandbox: auto 相当で構築し、allow_paths 配下への
// 書き込みが成功し、外側への書き込みが OS 層で拒否されることを確認する。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func teeArgs(path string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"command": "tee", "args": []string{path}})
	return b
}

func newExerciserShell(logger *slog.Logger, allowDir string) *tool.ShellTool {
	return tool.NewShell(config.ShellToolConfig{
		TimeoutSeconds:    5,
		MaxTimeoutSeconds: 5,
		AllowBinaries:     []string{"tee"},
		OSSandbox:         "auto",
	}, logger, []string{allowDir})
}

// checkAllowedWrite allowDir 配下への書込みが成功することを確認する
func checkAllowedWrite(ctx context.Context, sh *tool.ShellTool, allowDir string) (ok bool, detail string) {
	okPath := filepath.Join(allowDir, "ok.txt")
	res, err := sh.Execute(ctx, teeArgs(okPath))
	if err != nil {
		return false, err.Error()
	}
	if res.IsError {
		return false, res.Content
	}
	return true, ""
}

// checkDeniedWrite deniedDir 配下への書込みが OS 層で拒否されることを確認する。
// ShellTool.Execute が IsError を返すこと、かつファイルが実際に作られていないこと
// の両方を満たして初めて拒否成功とみなす
func checkDeniedWrite(ctx context.Context, sh *tool.ShellTool, deniedDir string) (blocked bool, detail string) {
	deniedPath := filepath.Join(deniedDir, "should_not_exist")
	res, err := sh.Execute(ctx, teeArgs(deniedPath))
	if err != nil {
		return false, err.Error()
	}
	if _, statErr := os.Stat(deniedPath); statErr == nil {
		return false, "file was created despite denial"
	}
	if !res.IsError {
		return false, "Execute did not report IsError"
	}
	return true, ""
}

func run(allowDir, deniedDir string, out, errOut *os.File) int {
	if allowDir == "" || deniedDir == "" {
		fmt.Fprintln(errOut, "-allow と -denied は必須です")
		return 2
	}
	logger := slog.New(slog.NewTextHandler(errOut, nil))
	sh := newExerciserShell(logger, allowDir)
	ctx := context.Background()

	if ok, detail := checkAllowedWrite(ctx, sh, allowDir); !ok {
		fmt.Fprintf(out, "write_to_allowed_ok=false detail=%s\n", detail)
		return 1
	}
	fmt.Fprintln(out, "write_to_allowed_ok=true")

	if blocked, detail := checkDeniedWrite(ctx, sh, deniedDir); !blocked {
		fmt.Fprintf(out, "write_to_denied_blocked=false detail=%s\n", detail)
		return 1
	}
	fmt.Fprintln(out, "write_to_denied_blocked=true")
	return 0
}

func main() {
	allow := flag.String("allow", "", "許可ディレクトリ")
	denied := flag.String("denied", "", "拒否されるべきディレクトリ")
	flag.Parse()
	os.Exit(run(*allow, *denied, os.Stdout, os.Stderr))
}
