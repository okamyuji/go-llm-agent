package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

// loadFromBody body を config.yaml として書き出して Load するヘルパ
func loadFromBody(t *testing.T, body string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load err=%v", err)
	}
	return cfg
}

func TestLoad_InstructionsDefaultsWhenUnspecified(t *testing.T) {
	cfg := loadFromBody(t, "default_model: test/m\n")
	if cfg.Agent.AgentsMD.MaxTotalBytes != 32768 {
		t.Fatalf("MaxTotalBytes = %d, want 32768 (coded default)", cfg.Agent.AgentsMD.MaxTotalBytes)
	}
	if cfg.Agent.AgentsMD.GlobalDir != "~/.go-llm-agent" {
		t.Fatalf("GlobalDir = %q, want ~/.go-llm-agent (coded default)", cfg.Agent.AgentsMD.GlobalDir)
	}
}

func TestLoad_MemoryDefaultsWhenUnspecified(t *testing.T) {
	cfg := loadFromBody(t, "default_model: test/m\n")
	m := cfg.Agent.Memory
	if m.Enabled == nil || !*m.Enabled {
		t.Fatalf("Enabled = %v, want true (coded default)", m.Enabled)
	}
	if m.Dir != "~/.go-llm-agent/projects" {
		t.Fatalf("Dir = %q, want ~/.go-llm-agent/projects (coded default)", m.Dir)
	}
	if m.IndexMaxLines != 200 {
		t.Fatalf("IndexMaxLines = %d, want 200 (coded default)", m.IndexMaxLines)
	}
	if m.IndexMaxBytes != 24576 {
		t.Fatalf("IndexMaxBytes = %d, want 24576 (coded default)", m.IndexMaxBytes)
	}
}

func TestLoad_MemoryExplicitValues(t *testing.T) {
	body := "default_model: test/m\n" +
		"agent:\n" +
		"  agents_md:\n" +
		"    max_total_bytes: 1024\n" +
		"    global_dir: /tmp/g\n" +
		"  memory:\n" +
		"    enabled: false\n" +
		"    dir: /tmp/mem\n" +
		"    index_max_lines: 10\n" +
		"    index_max_bytes: 100\n"
	cfg := loadFromBody(t, body)
	if cfg.Agent.AgentsMD.MaxTotalBytes != 1024 || cfg.Agent.AgentsMD.GlobalDir != "/tmp/g" {
		t.Fatalf("agents_md 明示値が反映されない: %+v", cfg.Agent.AgentsMD)
	}
	m := cfg.Agent.Memory
	if m.Enabled == nil || *m.Enabled {
		t.Fatalf("enabled: false が上書きされた: %v", m.Enabled)
	}
	if m.Dir != "/tmp/mem" || m.IndexMaxLines != 10 || m.IndexMaxBytes != 100 {
		t.Fatalf("memory 明示値が反映されない: %+v", m)
	}
}
