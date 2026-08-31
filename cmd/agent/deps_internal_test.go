package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/obs"
	"github.com/okamyuji/go-llm-agent/internal/secret"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func testLogger() *slog.Logger {
	return obs.NewLogger(obs.LoggerOptions{Format: "text", Level: "error"})
}

func TestInitTelemetry_DisabledIsNoop(t *testing.T) {
	if err := initTelemetry(context.Background(), &config.Config{}); err != nil {
		t.Fatalf("OTel 無効時はエラーなし期待 got %v", err)
	}
}

func TestResolveProviderAPIKey_ReturnsValue(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "sk-value")
	got, err := resolveProviderAPIKey(secret.NewResolver(".env"), "openai", "TEST_PROVIDER_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-value" {
		t.Fatalf("env の値をそのまま返す期待 got %q", got)
	}
}

func TestResolveProviderAPIKey_EmptyValueIsError(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY_EMPTY", "")
	_, err := resolveProviderAPIKey(secret.NewResolver(".env"), "openai", "TEST_PROVIDER_KEY_EMPTY")
	if err == nil {
		t.Fatal("空キーはエラー期待")
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Fatalf("プロバイダー名を含む期待 got %v", err)
	}
}

// providerConfigs 全プロバイダーを 1 つずつ定義した config を作る
func providerConfigs() *config.Config {
	cfg := &config.Config{}
	cfg.Providers = map[string]config.ProviderConfig{
		"openai":    {BaseURL: "http://127.0.0.1:1", APIKeyEnv: "TEST_OPENAI_KEY", AllowModels: []string{"gpt-4.1-mini"}},
		"anthropic": {BaseURL: "http://127.0.0.1:1", APIKeyEnv: "TEST_ANTHROPIC_KEY", FallbackTo: "openai"},
		"gemini":    {BaseURL: "http://127.0.0.1:1", APIKeyEnv: "TEST_GEMINI_KEY"},
		"ollama":    {BaseURL: "http://127.0.0.1:1"},
		"llamacpp":  {BaseURL: "http://127.0.0.1:1"},
	}
	return cfg
}

func TestBuildProviders_AllProviders(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY", "a")
	t.Setenv("TEST_ANTHROPIC_KEY", "b")
	t.Setenv("TEST_GEMINI_KEY", "c")
	provs, err := buildProviders(providerConfigs(), secret.NewResolver(".env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openai", "anthropic", "gemini", "ollama", "llamacpp"} {
		if provs[name] == nil {
			t.Fatalf("%s が構築される期待", name)
		}
	}
}

func TestBuildProviders_EmptyConfigBuildsNothing(t *testing.T) {
	provs, err := buildProviders(&config.Config{}, secret.NewResolver(".env"))
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 0 {
		t.Fatalf("プロバイダー未定義なら空 map 期待 got %d", len(provs))
	}
}

func TestBuildProviders_MissingKeyErrors(t *testing.T) {
	cases := map[string]string{
		"openai":    "TEST_MISSING_OPENAI",
		"anthropic": "TEST_MISSING_ANTHROPIC",
		"gemini":    "TEST_MISSING_GEMINI",
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Providers = map[string]config.ProviderConfig{name: {APIKeyEnv: env}}
			if _, err := buildProviders(cfg, secret.NewResolver(".env")); err == nil {
				t.Fatalf("%s のキー未解決はエラー期待", name)
			}
		})
	}
}

func TestBuildLLMRegistry_AllowModelsAndFallback(t *testing.T) {
	t.Setenv("TEST_OPENAI_KEY", "a")
	t.Setenv("TEST_ANTHROPIC_KEY", "b")
	t.Setenv("TEST_GEMINI_KEY", "c")
	cfg := providerConfigs()
	provs, err := buildProviders(cfg, secret.NewResolver(".env"))
	if err != nil {
		t.Fatal(err)
	}
	reg := buildLLMRegistry(cfg, provs)
	if _, _, err := reg.Resolve("openai/gpt-4.1-mini"); err != nil {
		t.Fatalf("allow_models 内のモデルは解決できる期待 got %v", err)
	}
	if _, _, err := reg.Resolve("openai/not-allowed"); err == nil {
		t.Fatal("allow_models 外のモデルは拒否される期待")
	}
	if p, _, _, _, err := reg.ResolveWithFallback("anthropic/claude"); err != nil || p == nil {
		t.Fatalf("fallback_to 付きプロバイダーも解決できる期待 got p=%v err=%v", p, err)
	}
}

func TestBuildToolList_ContainsBuiltins(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.EnabledTools = []string{"web_fetch"}
	cfg.Tools.FS.AllowPaths = []string{t.TempDir()}
	sb := tool.NewSandboxWithDeny(cfg.Tools.FS.AllowPaths, nil)
	tools := buildToolList(cfg, testLogger(), sb, tool.NewReadRegistry())
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Spec().Name] = true
	}
	for _, want := range []string{"fs_read", "fs_write", "fs_edit", "shell", "http_fetch", "search_files", "web_search", "web_fetch"} {
		if !names[want] {
			t.Fatalf("%s が含まれる期待 got %v", want, names)
		}
	}
}

func TestAttachNoteTools_AddsTwoTools(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.SessionsDir = t.TempDir()
	got, err := attachNoteTools(cfg, testLogger(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("note_add と note_search の 2 件追加期待 got %d", len(got))
	}
}

func TestAttachNoteTools_ExplicitNotesPath(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.NotesPath = filepath.Join(t.TempDir(), "notes.jsonl")
	got, err := attachNoteTools(cfg, testLogger(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("notes_path 指定でも 2 件追加期待 got %d", len(got))
	}
}

// unopenableNotesPath ファイルを親ディレクトリに見立てた開けないパスを作る
func unopenableNotesPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, "notes.jsonl")
}

func TestAttachNoteTools_StrictInitFails(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.NotesPath = unopenableNotesPath(t)
	cfg.Storage.StrictNotesInit = true
	if _, err := attachNoteTools(cfg, testLogger(), nil); err == nil {
		t.Fatal("strict_notes_init=true はエラー期待")
	}
}

func TestAttachNoteTools_DegradedModeKeepsTools(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.NotesPath = unopenableNotesPath(t)
	got, err := attachNoteTools(cfg, testLogger(), []tool.Tool{})
	if err != nil {
		t.Fatalf("degraded mode はエラーにしない期待 got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ノートツールを追加しない期待 got %d", len(got))
	}
}

func TestResolveEnabledTools_ServeExcludesFSEdit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.EnabledTools = []string{"fs_read", "fs_edit", "shell"}
	got := resolveEnabledTools(cfg, true)
	if len(got) != 2 || got[0] != "fs_read" || got[1] != "shell" {
		t.Fatalf("serve では fs_edit を除外する期待 got %v", got)
	}
	if len(cfg.Agent.EnabledTools) != 3 {
		t.Fatal("元スライスを変更しない期待")
	}
}

func TestResolveEnabledTools_ChatKeepsFSEdit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.EnabledTools = []string{"fs_read", "fs_edit"}
	if got := resolveEnabledTools(cfg, false); len(got) != 2 {
		t.Fatalf("chat では除外しない期待 got %v", got)
	}
}

func TestResolveEnabledTools_ServeWithoutFSEdit(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.EnabledTools = []string{"fs_read"}
	if got := resolveEnabledTools(cfg, true); len(got) != 1 {
		t.Fatalf("fs_edit 未指定なら素通し期待 got %v", got)
	}
}

func TestResolveModel(t *testing.T) {
	cfg := &config.Config{DefaultModel: "openai/gpt-4.1-mini"}
	if got := resolveModel(cfg, "ollama/qwen3"); got != "ollama/qwen3" {
		t.Fatalf("上書き優先期待 got %q", got)
	}
	if got := resolveModel(cfg, ""); got != "openai/gpt-4.1-mini" {
		t.Fatalf("既定モデル期待 got %q", got)
	}
}

func TestLoadDeps_BuildsRegistries(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	cfg, reg, tools, store, _, err := loadDeps(context.Background(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || reg == nil || tools == nil || store == nil {
		t.Fatal("全依存が非 nil 期待")
	}
}

func TestLoadDeps_ConfigErrorPropagates(t *testing.T) {
	if _, _, _, _, _, err := loadDeps(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), false); err == nil {
		t.Fatal("設定読み込み失敗はエラー期待")
	}
}

func TestBuildServiceDeps_BuildsService(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	deps, err := buildServiceDeps(context.Background(), path, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if deps.svc == nil {
		t.Fatal("agent.Service が構築される期待")
	}
	if deps.model != "openai/gpt-4.1-mini" {
		t.Fatalf("既定モデルを解決する期待 got %q", deps.model)
	}
}

func TestBuildServiceDeps_ModelOverride(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	deps, err := buildServiceDeps(context.Background(), path, "ollama/qwen3", false)
	if err != nil {
		t.Fatal(err)
	}
	if deps.model != "ollama/qwen3" {
		t.Fatalf("上書きモデル期待 got %q", deps.model)
	}
}

func TestBuildServiceDeps_ConfigErrorPropagates(t *testing.T) {
	if _, err := buildServiceDeps(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), "", false); err == nil {
		t.Fatal("設定読み込み失敗はエラー期待")
	}
}
