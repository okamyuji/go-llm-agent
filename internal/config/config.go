package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 全体設定
type Config struct {
	DefaultModel string                    `yaml:"default_model"`
	Providers    map[string]ProviderConfig `yaml:"providers"`
	Agent        AgentConfig               `yaml:"agent"`
	Tools        ToolsConfig               `yaml:"tools"`
	Server       ServerConfig              `yaml:"server"`
	Storage      StorageConfig             `yaml:"storage"`
	Logging      LoggingConfig             `yaml:"logging"`
}

// ProviderConfig プロバイダー固有設定
type ProviderConfig struct {
	BaseURL     string   `yaml:"base_url"`
	APIKeyEnv   string   `yaml:"api_key_env"`
	AllowModels []string `yaml:"allow_models"`
}

// AgentConfig エージェントループ設定
type AgentConfig struct {
	MaxToolHops  int      `yaml:"max_tool_hops"`
	EnabledTools []string `yaml:"enabled_tools"`
	SystemPrompt string   `yaml:"system_prompt"`
}

// ToolsConfig ツール群の設定
type ToolsConfig struct {
	FS          FSToolConfig        `yaml:"fs"`
	Shell       ShellToolConfig     `yaml:"shell"`
	HTTPFetch   HTTPFetchToolConfig `yaml:"http_fetch"`
	SearchFiles SearchFilesConfig   `yaml:"search_files"`
}

// FSToolConfig fs_read と fs_write の設定
type FSToolConfig struct {
	AllowPaths   []string `yaml:"allow_paths"`
	DenyPaths    []string `yaml:"deny_paths"`
	MaxReadBytes int      `yaml:"max_read_bytes"`
}

// ShellToolConfig shell の設定
type ShellToolConfig struct {
	TimeoutSeconds    int      `yaml:"timeout_seconds"`
	MaxTimeoutSeconds int      `yaml:"max_timeout_seconds"`
	AllowBinaries     []string `yaml:"allow_binaries"`
	ArgDenyPatterns   []string `yaml:"arg_deny_patterns"`
}

// HTTPFetchToolConfig http_fetch の設定
type HTTPFetchToolConfig struct {
	DenyPrivateNetworks bool     `yaml:"deny_private_networks"`
	TimeoutSeconds      int      `yaml:"timeout_seconds"`
	MaxBodyBytes        int      `yaml:"max_body_bytes"`
	AllowDomains        []string `yaml:"allow_domains"`
}

// SearchFilesConfig search_files の設定
type SearchFilesConfig struct {
	MaxResults int `yaml:"max_results"`
}

// ServerConfig HTTP サーバ設定
type ServerConfig struct {
	Addr string `yaml:"addr"`
}

// StorageConfig ストレージ設定
type StorageConfig struct {
	SessionsDir string `yaml:"sessions_dir"`
}

// LoggingConfig ログ設定
type LoggingConfig struct {
	Format string `yaml:"format"`
	Level  string `yaml:"level"`
}

// Load 指定パスから設定を読み込む
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config read: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("config parse: %w", err)
	}
	return &cfg, nil
}
