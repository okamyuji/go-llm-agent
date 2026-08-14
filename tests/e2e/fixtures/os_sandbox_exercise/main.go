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

func main() {
	allow := flag.String("allow", "", "許可ディレクトリ")
	denied := flag.String("denied", "", "拒否されるべきディレクトリ")
	flag.Parse()
	if *allow == "" || *denied == "" {
		fmt.Fprintln(os.Stderr, "-allow と -denied は必須です")
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	sh := tool.NewShell(config.ShellToolConfig{
		TimeoutSeconds:    5,
		MaxTimeoutSeconds: 5,
		AllowBinaries:     []string{"tee"},
		OSSandbox:         "auto",
	}, logger, []string{*allow})

	okPath := filepath.Join(*allow, "ok.txt")
	res, err := sh.Execute(context.Background(), teeArgs(okPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "allow write err:", err)
		os.Exit(1)
	}
	if res.IsError {
		fmt.Printf("write_to_allowed_ok=false detail=%s\n", res.Content)
		os.Exit(1)
	}
	fmt.Println("write_to_allowed_ok=true")

	deniedPath := filepath.Join(*denied, "should_not_exist")
	res2, err := sh.Execute(context.Background(), teeArgs(deniedPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "denied write err:", err)
		os.Exit(1)
	}
	if _, statErr := os.Stat(deniedPath); statErr == nil {
		fmt.Println("write_to_denied_blocked=false")
		os.Exit(1)
	}
	if !res2.IsError {
		fmt.Println("write_to_denied_blocked=false")
		os.Exit(1)
	}
	fmt.Println("write_to_denied_blocked=true")
}
