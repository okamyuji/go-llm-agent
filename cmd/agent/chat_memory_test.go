package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// instructionsCfg AGENTS.md 階層読込を有効化した最小 config を返す
func instructionsCfg(globalDir string, allow []string) *config.Config {
	cfg := &config.Config{}
	cfg.Agent.SystemPrompt = "base"
	cfg.Agent.AgentsMD = config.AgentsMDConfig{Enabled: boolPtr(true), MaxBytes: 1000, MaxTotalBytes: 100000, GlobalDir: globalDir}
	cfg.Tools.FS.AllowPaths = allow
	cfg.Agent.Memory = config.MemoryConfig{Enabled: boolPtr(false)}
	return cfg
}

func TestResolveInstructions_NoFilesKeepsBase(t *testing.T) {
	dir := resolvedTempDir(t)
	withChdir(t, dir)
	cfg := instructionsCfg("", []string{dir})

	sysPrompt, paths, err := resolveInstructions(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if sysPrompt != "base" || len(paths) != 0 {
		t.Fatalf("sysPrompt=%q paths=%v", sysPrompt, paths)
	}
}

func TestResolveInstructions_GlobalThenProjectWithMarkers(t *testing.T) {
	dir := resolvedTempDir(t)
	global := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(global, "AGENTS.md"), []byte("GLOBAL RULE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("PROJECT RULE"), 0o600); err != nil {
		t.Fatal(err)
	}
	withChdir(t, dir)
	cfg := instructionsCfg(global, []string{dir})

	sysPrompt, paths, err := resolveInstructions(cfg)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	gi := strings.Index(sysPrompt, "GLOBAL RULE")
	pi := strings.Index(sysPrompt, "PROJECT RULE")
	if gi < 0 || pi < 0 || gi > pi {
		t.Fatalf("順序が global→project でない: %q", sysPrompt)
	}
	if strings.Count(sysPrompt, "[UNTRUSTED PROJECT FILE: AGENTS.md") != 2 {
		t.Fatalf("ファイルごとのマーカーが無い: %q", sysPrompt)
	}
	if !strings.HasPrefix(sysPrompt, "base") {
		t.Fatalf("base が先頭でない: %q", sysPrompt)
	}
	if len(paths) != 2 {
		t.Fatalf("paths=%v", paths)
	}
}

func TestResolveInstructions_DisabledSkips(t *testing.T) {
	dir := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("must not"), 0o600); err != nil {
		t.Fatal(err)
	}
	withChdir(t, dir)
	cfg := instructionsCfg("", []string{dir})
	cfg.Agent.AgentsMD.Enabled = boolPtr(false)

	sysPrompt, paths, err := resolveInstructions(cfg)
	if err != nil || sysPrompt != "base" || len(paths) != 0 {
		t.Fatalf("sysPrompt=%q paths=%v err=%v", sysPrompt, paths, err)
	}
}

// memoryCfg 自動メモリを有効化した最小 config を返す
func memoryCfg(dir string) *config.Config {
	cfg := &config.Config{}
	cfg.Agent.Memory = config.MemoryConfig{Enabled: boolPtr(true), Dir: dir, IndexMaxLines: 200, IndexMaxBytes: 24576}
	return cfg
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestResolveMemory_InjectsIndexWithMarker(t *testing.T) {
	work := resolvedTempDir(t)
	withChdir(t, work)
	memRoot := resolvedTempDir(t)
	cfg := memoryCfg(memRoot)

	store, section := resolveMemory(cfg, quietLogger())
	if store == nil {
		t.Fatalf("store が nil")
	}
	if section != "" {
		t.Fatalf("索引が無いのに注入された: %q", section)
	}
	if err := store.Write("MEMORY.md", "- remembered fact", false); err != nil {
		t.Fatalf("Write: %v", err)
	}

	store2, section2 := resolveMemory(cfg, quietLogger())
	if store2 == nil {
		t.Fatalf("store2 が nil")
	}
	if !strings.Contains(section2, "remembered fact") || !strings.Contains(section2, "[AGENT MEMORY") {
		t.Fatalf("索引とマーカーが注入されていない: %q", section2)
	}
}

func TestResolveMemory_DisabledReturnsNil(t *testing.T) {
	work := resolvedTempDir(t)
	withChdir(t, work)
	cfg := memoryCfg(resolvedTempDir(t))
	cfg.Agent.Memory.Enabled = boolPtr(false)

	store, section := resolveMemory(cfg, quietLogger())
	if store != nil || section != "" {
		t.Fatalf("無効時に store=%v section=%q", store, section)
	}
}

func TestResolveMemory_DegradedOnInitFailure(t *testing.T) {
	work := resolvedTempDir(t)
	withChdir(t, work)
	blocker := filepath.Join(resolvedTempDir(t), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 通常ファイルの配下にはディレクトリを作れないため初期化に失敗する
	cfg := memoryCfg(blocker)

	store, section := resolveMemory(cfg, quietLogger())
	if store != nil || section != "" {
		t.Fatalf("degraded にならない: store=%v section=%q", store, section)
	}
}

func TestAttachMemoryTools(t *testing.T) {
	work := resolvedTempDir(t)
	withChdir(t, work)
	cfg := memoryCfg(resolvedTempDir(t))

	tools := attachMemoryTools(cfg, quietLogger(), nil)
	names := toolNames(tools)
	if !strings.Contains(names, "memory_write") || !strings.Contains(names, "memory_read") {
		t.Fatalf("メモリツールが登録されない: %s", names)
	}

	cfg.Agent.Memory.Enabled = boolPtr(false)
	if got := attachMemoryTools(cfg, quietLogger(), nil); len(got) != 0 {
		t.Fatalf("無効時にツールが登録された: %s", toolNames(got))
	}
}

func TestAttachMemoryTools_DegradedOnInitFailure(t *testing.T) {
	work := resolvedTempDir(t)
	withChdir(t, work)
	blocker := filepath.Join(resolvedTempDir(t), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := memoryCfg(blocker)

	if got := attachMemoryTools(cfg, quietLogger(), nil); len(got) != 0 {
		t.Fatalf("初期化失敗時にツールが登録された: %s", toolNames(got))
	}
}

func toolNames(tools []tool.Tool) string {
	var b []string
	for _, tl := range tools {
		b = append(b, tl.Spec().Name)
	}
	return strings.Join(b, ",")
}
