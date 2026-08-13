package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

const sampleYAML = `
default_model: openai/gpt-4.1-mini

providers:
  openai:
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY
  ollama:
    base_url: http://localhost:11434

agent:
  max_tool_hops: 8
  enabled_tools: [fs_read, shell]
  system_prompt: "you are helpful"

tools:
  shell:
    timeout_seconds: 30
    max_timeout_seconds: 300
    allow_binaries: [git, go]
  http_fetch:
    deny_private_networks: true
    timeout_seconds: 15

server:
  addr: 127.0.0.1:14000

storage:
  sessions_dir: /tmp/go-llm-agent-sessions
`

func TestLoad_ParsesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(sampleYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load err=%v", err)
	}
	if cfg.DefaultModel != "openai/gpt-4.1-mini" {
		t.Fatalf("default_model got=%q", cfg.DefaultModel)
	}
	if cfg.Providers["openai"].APIKeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("openai api_key_env")
	}
	if cfg.Agent.MaxToolHops != 8 {
		t.Fatalf("max_tool_hops")
	}
	if len(cfg.Agent.EnabledTools) != 2 {
		t.Fatalf("enabled_tools")
	}
}

func TestLoad_FileMissing(t *testing.T) {
	_, err := config.Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("missing file は err")
	}
}

const hardenedYAML = `
default_model: openai/gpt-4.1-mini

providers:
  openai:
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY
    allow_models: [gpt-4.1-mini, gpt-4o-mini]

agent:
  max_tool_hops: 4
  enabled_tools: [fs_read, search_files]

tools:
  fs:
    allow_paths: [/tmp/agent-workspace]
    deny_paths: [".git", ".env"]
  shell:
    allow_binaries: [git]
    arg_deny_patterns:
      - 'config\s+--global'
  http_fetch:
    deny_private_networks: true
    allow_domains: [example.com]
`

const promptFileYAML = `
default_model: openai/gpt-4.1-mini

providers:
  openai:
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY

agent:
  max_tool_hops: 4
  enabled_tools: [fs_read]
  system_prompt_file: prompts/my-system.md

tools:
  shell:
    allow_binaries: [git]

server:
  addr: 127.0.0.1:14000
`

func TestLoad_SystemPromptFile(t *testing.T) {
	dir := t.TempDir()
	promptDir := filepath.Join(dir, "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	promptContent := "You are a helpful assistant.\nBe concise."
	if err := os.WriteFile(filepath.Join(promptDir, "my-system.md"), []byte(promptContent), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(promptFileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load err=%v", err)
	}
	if cfg.Agent.SystemPrompt != promptContent {
		t.Fatalf("system_prompt got=%q want=%q", cfg.Agent.SystemPrompt, promptContent)
	}
}

func TestLoad_SystemPromptFileMissing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(promptFileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(cfgPath)
	if err == nil {
		t.Fatal("missing prompt file should error")
	}
}

const conflictYAML = `
default_model: openai/gpt-4.1-mini

providers:
  openai:
    base_url: https://api.openai.com/v1
    api_key_env: OPENAI_API_KEY

agent:
  max_tool_hops: 4
  enabled_tools: [fs_read]
  system_prompt: "inline prompt"
  system_prompt_file: prompts/my-system.md

tools:
  shell:
    allow_binaries: [git]

server:
  addr: 127.0.0.1:14000
`

func TestLoad_SystemPromptConflict(t *testing.T) {
	dir := t.TempDir()
	promptDir := filepath.Join(dir, "prompts")
	if err := os.MkdirAll(promptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "my-system.md"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(conflictYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(cfgPath)
	if err == nil {
		t.Fatal("both system_prompt and system_prompt_file should error")
	}
}

func TestLoad_HardeningFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(hardenedYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load err=%v", err)
	}
	if got := cfg.Providers["openai"].AllowModels; len(got) != 2 || got[0] != "gpt-4.1-mini" {
		t.Fatalf("allow_models got=%v", got)
	}
	if got := cfg.Tools.FS.DenyPaths; len(got) != 2 || got[0] != ".git" {
		t.Fatalf("deny_paths got=%v", got)
	}
	if got := cfg.Tools.Shell.ArgDenyPatterns; len(got) != 1 || got[0] != `config\s+--global` {
		t.Fatalf("arg_deny_patterns got=%v", got)
	}
	if got := cfg.Tools.HTTPFetch.AllowDomains; len(got) != 1 || got[0] != "example.com" {
		t.Fatalf("allow_domains got=%v", got)
	}
}

func TestLoad_RejectsInvalidToolCallIDFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "providers:\n  llamacpp:\n    base_url: http://localhost:8080/v1\n    tool_call_id_format: alnum-9\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("want error for invalid tool_call_id_format, got nil")
	}
	if !strings.Contains(err.Error(), "tool_call_id_format") {
		t.Errorf("error should mention tool_call_id_format, got: %v", err)
	}
}

func TestLoad_AcceptsValidToolCallIDFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "providers:\n  llamacpp:\n    base_url: http://localhost:8080/v1\n    tool_call_id_format: alnum9\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatalf("valid alnum9 rejected: %v", err)
	}
}

func TestLoad_WebToolsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "default_model: test/m\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load err=%v", err)
	}
	ws := cfg.Tools.WebSearch
	if ws.Endpoint != "" || ws.MaxResults != 0 {
		t.Errorf("web_search はゼロ値のまま (既定はツール側で適用): %+v", ws)
	}
	wf := cfg.Tools.WebFetch
	if wf.WebgrabPath != "" || wf.MaxChars != 0 {
		t.Errorf("web_fetch はゼロ値のまま: %+v", wf)
	}
}

func TestLoad_WebSearchRejectsOutOfRangeMaxResults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "tools:\n  web_search:\n    max_results: 11\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "max_results") {
		t.Fatalf("want max_results range error, got %v", err)
	}
}

func TestLoad_WebSearchRejectsNonHTTPEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "tools:\n  web_search:\n    endpoint: ftp://example.com/\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("want endpoint scheme error, got %v", err)
	}
}

func TestLoad_WebFetchRejectsTooSmallMaxChars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "tools:\n  web_fetch:\n    max_chars: 99\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "max_chars") {
		t.Fatalf("want max_chars range error, got %v", err)
	}
}

func TestLoad_WebToolsAcceptsValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "tools:\n  web_search:\n    endpoint: https://html.duckduckgo.com/html/\n    max_results: 5\n  web_fetch:\n    webgrab_path: /usr/local/bin/webgrab\n    max_chars: 4000\n    allow_domains: [example.com]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("valid web tools config rejected: %v", err)
	}
	if cfg.Tools.WebFetch.AllowDomains[0] != "example.com" {
		t.Errorf("allow_domains not parsed: %+v", cfg.Tools.WebFetch)
	}
}

func TestLoad_WebSearchRejectsHostlessEndpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "tools:\n  web_search:\n    endpoint: \"https:///path\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("want hostless endpoint error, got %v", err)
	}
}

func TestLoad_ToolResultLimitDefaultsWhenUnspecified(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "default_model: test/m\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load err=%v", err)
	}
	if cfg.Agent.ToolResultLimit.MaxChars != 8000 {
		t.Fatalf("got %d, want 8000 (coded default)", cfg.Agent.ToolResultLimit.MaxChars)
	}
}

func TestLoad_ToolResultLimitMinusOneDisablesTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "agent:\n  tool_result_limit:\n    max_chars: -1\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load err=%v", err)
	}
	if cfg.Agent.ToolResultLimit.MaxChars != -1 {
		t.Fatalf("got %d, want -1 (kept, not overwritten by default)", cfg.Agent.ToolResultLimit.MaxChars)
	}
}

func TestLoad_ToolResultLimitRejectsBelowMinusOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "agent:\n  tool_result_limit:\n    max_chars: -2\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil || !strings.Contains(err.Error(), "max_chars") {
		t.Fatalf("want max_chars error, got %v", err)
	}
}
