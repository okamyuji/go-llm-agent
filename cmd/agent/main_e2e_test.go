package main_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2E_Run_OpenAIStubbed(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi from stub\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "default_model: openai/gpt-4.1-mini\n" +
		"providers:\n" +
		"  openai:\n" +
		"    base_url: " + srv.URL + "\n" +
		"    api_key_env: OPENAI_API_KEY\n" +
		"agent:\n" +
		"  max_tool_hops: 2\n" +
		"  enabled_tools: []\n" +
		"tools:\n" +
		"  fs:\n" +
		"    allow_paths: [\"" + dir + "\"]\n" +
		"  shell:\n" +
		"    timeout_seconds: 5\n" +
		"    max_timeout_seconds: 10\n" +
		"    allow_binaries: []\n" +
		"  http_fetch: {}\n" +
		"  search_files: {}\n" +
		"server:\n" +
		"  addr: 127.0.0.1:0\n" +
		"storage:\n" +
		"  sessions_dir: " + dir + "\n" +
		"logging:\n" +
		"  format: text\n" +
		"  level: info\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(dir, "agent")
	buildCtx, buildCancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "run", "--config", cfgPath, "-p", "say hi")
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY=dummy")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v output=%s", err, out.String())
	}
	if !strings.Contains(out.String(), "hi from stub") {
		t.Fatalf("stub の出力が含まれない: %q", out.String())
	}
}

func TestE2E_VersionCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "agent")
	buildCtx, buildCancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer buildCancel()
	build := exec.CommandContext(buildCtx, "go", "build", "-o", binary, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	runCtx, runCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer runCancel()
	out, err := exec.CommandContext(runCtx, binary, "version").Output()
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Fatalf("version empty")
	}
}
