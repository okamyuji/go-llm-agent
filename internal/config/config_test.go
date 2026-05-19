package config_test

import (
	"os"
	"path/filepath"
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
