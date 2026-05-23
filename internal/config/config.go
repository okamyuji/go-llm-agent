package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 全体設定
type Config struct {
	DefaultModel  string                    `yaml:"default_model"`
	Providers     map[string]ProviderConfig `yaml:"providers"`
	Agent         AgentConfig               `yaml:"agent"`
	Tools         ToolsConfig               `yaml:"tools"`
	Server        ServerConfig              `yaml:"server"`
	Storage       StorageConfig             `yaml:"storage"`
	Logging       LoggingConfig             `yaml:"logging"`
	Observability ObservabilityConfig       `yaml:"observability"`
	Safety        SafetyConfig              `yaml:"safety"`
}

// SafetyConfig 入出力フィルタとリダクタの設定
type SafetyConfig struct {
	InputScanner   SafetyInputScanner   `yaml:"input_scanner"`
	OutputRedactor SafetyOutputRedactor `yaml:"output_redactor"`
	PIIRedactor    SafetyPIIRedactor    `yaml:"pii_redactor"`
}

// SafetyPIIRedactor PII リダクタの設定
type SafetyPIIRedactor struct {
	Enabled bool                    `yaml:"enabled"`
	Rules   []SafetyPIIRedactorRule `yaml:"rules"`
}

// SafetyPIIRedactorRule PII リダクタの 1 ルール
type SafetyPIIRedactorRule struct {
	ID          string `yaml:"id"`
	Regex       string `yaml:"regex"`
	Replacement string `yaml:"replacement"`
}

// SafetyInputScanner 入力スキャナの設定
type SafetyInputScanner struct {
	Enabled      bool                     `yaml:"enabled"`
	BlockOnMatch bool                     `yaml:"block_on_match"`
	Patterns     []SafetyInputScannerRule `yaml:"patterns"`
}

// SafetyInputScannerRule 入力スキャナの 1 ルール
type SafetyInputScannerRule struct {
	ID    string `yaml:"id"`
	Regex string `yaml:"regex"`
}

// SafetyOutputRedactor 出力リダクタの設定
type SafetyOutputRedactor struct {
	Enabled bool                       `yaml:"enabled"`
	Rules   []SafetyOutputRedactorRule `yaml:"rules"`
}

// SafetyOutputRedactorRule 出力リダクタの 1 ルール
type SafetyOutputRedactorRule struct {
	ID          string `yaml:"id"`
	Regex       string `yaml:"regex"`
	Replacement string `yaml:"replacement"`
}

// ObservabilityConfig OTel 計装の設定をまとめる
type ObservabilityConfig struct {
	OTel OTelConfig `yaml:"otel"`
}

// OTelConfig OTLP HTTP exporter とサンプリングの設定
type OTelConfig struct {
	Enabled                bool    `yaml:"enabled"`
	Exporter               string  `yaml:"exporter"`
	Endpoint               string  `yaml:"endpoint"`
	Insecure               bool    `yaml:"insecure"`
	SampleRatio            float64 `yaml:"sample_ratio"`
	ServiceName            string  `yaml:"service_name"`
	MetricsIntervalSeconds int     `yaml:"metrics_interval_seconds"`
}

// ProviderConfig プロバイダー固有設定
type ProviderConfig struct {
	BaseURL               string        `yaml:"base_url"`
	APIKeyEnv             string        `yaml:"api_key_env"`
	AllowModels           []string      `yaml:"allow_models"`
	Pricing               PricingConfig `yaml:"pricing"`
	RequestTimeoutSeconds int           `yaml:"request_timeout_seconds"`
	Retry                 RetryConfig   `yaml:"retry"`
	FallbackTo            string        `yaml:"fallback_to"`
}

// RetryConfig リトライ設定。MaxAttempts<=1 でリトライ無効
type RetryConfig struct {
	MaxAttempts      int     `yaml:"max_attempts"`
	InitialBackoffMS int     `yaml:"initial_backoff_ms"`
	MaxBackoffMS     int     `yaml:"max_backoff_ms"`
	JitterRatio      float64 `yaml:"jitter_ratio"`
}

// PricingConfig 1 プロバイダーあたりの単価設定 (JPY per million tokens)
type PricingConfig struct {
	InputPerMillionJPY  float64 `yaml:"input_per_million_jpy"`
	OutputPerMillionJPY float64 `yaml:"output_per_million_jpy"`
}

// AgentConfig エージェントループ設定
type AgentConfig struct {
	MaxToolHops     int                   `yaml:"max_tool_hops"`
	EnabledTools    []string              `yaml:"enabled_tools"`
	SystemPrompt    string                `yaml:"system_prompt"`
	Budget          BudgetConfig          `yaml:"budget"`
	ToolChoice      ToolChoiceConfig      `yaml:"tool_choice"`
	ToolValidation  ToolValidationConfig  `yaml:"tool_validation"`
	Approval        ApprovalConfig        `yaml:"approval"`
	Strategy        string                `yaml:"strategy"`
	PlannerExecutor PlannerExecutorConfig `yaml:"planner_executor"`
	Reflection      ReflectionConfig      `yaml:"reflection"`
	ParallelTools   ParallelToolsConfig   `yaml:"parallel_tools"`
}

// ParallelToolsConfig 並列ツール実行の設定
// require_approval ツールが含まれる場合はバリア方式で自動的に直列化する
type ParallelToolsConfig struct {
	Enabled        bool `yaml:"enabled"`
	MaxConcurrency int  `yaml:"max_concurrency"`
	FailFast       bool `yaml:"fail_fast"`
}

// PlannerExecutorConfig Planner-Executor 戦略の設定
type PlannerExecutorConfig struct {
	PlannerModel  string `yaml:"planner_model"`
	ExecutorModel string `yaml:"executor_model"`
	MaxSteps      int    `yaml:"max_steps"`
}

// ReflectionConfig Reflection 戦略のトリガー設定
type ReflectionConfig struct {
	MaxIterations       int `yaml:"max_iterations"`
	ConsecutiveFailures int `yaml:"trigger_consecutive_failures"`
	HopBudget           int `yaml:"trigger_hop_budget"`
}

// ApprovalConfig HITL ツール承認の設定
// default_decision は "deny" または "allow"。
// required_tools は "shell" や "fs_write" など承認必須のツール名一覧
type ApprovalConfig struct {
	RequiredTools   []string `yaml:"required_tools"`
	TimeoutSeconds  int      `yaml:"timeout_seconds"`
	DefaultDecision string   `yaml:"default_decision"`
}

// ToolChoiceConfig tool_choice の設定
type ToolChoiceConfig struct {
	Mode string `yaml:"mode"`
	Name string `yaml:"name"`
}

// ToolValidationConfig tool 引数 JSON Schema 検証の設定
type ToolValidationConfig struct {
	Enabled    bool `yaml:"enabled"`
	MaxRetries int  `yaml:"max_retries"`
}

// BudgetConfig 予算上限の設定。0 は無制限として扱う
type BudgetConfig struct {
	SessionMaxTokens int     `yaml:"session_max_tokens"`
	DailyMaxCostJPY  float64 `yaml:"daily_max_cost_jpy"`
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
	Addr      string           `yaml:"addr"`
	Auth      ServerAuthConfig `yaml:"auth"`
	RateLimit ServerRateLimit  `yaml:"rate_limit"`
	Allowlist ServerAllowlist  `yaml:"allowlist"`
	CORS      ServerCORS       `yaml:"cors"`
}

// ServerAuthConfig Bearer Token 認証設定
type ServerAuthConfig struct {
	Enabled      bool                `yaml:"enabled"`
	BearerTokens []ServerBearerToken `yaml:"bearer_tokens"`
}

// ServerBearerToken 個別の Bearer Token エントリ。secret_env からのみ値を解決する
type ServerBearerToken struct {
	ID        string `yaml:"id"`
	SecretEnv string `yaml:"secret_env"`
}

// ServerRateLimit token bucket レート制限の設定
type ServerRateLimit struct {
	Enabled  bool    `yaml:"enabled"`
	RPS      float64 `yaml:"rps"`
	Burst    int     `yaml:"burst"`
	PerToken bool    `yaml:"per_token"`
}

// ServerAllowlist IP allowlist の設定
type ServerAllowlist struct {
	CIDRs []string `yaml:"cidrs"`
}

// ServerCORS CORS ヘッダ設定
type ServerCORS struct {
	Enabled      bool     `yaml:"enabled"`
	AllowOrigins []string `yaml:"allow_origins"`
	AllowMethods []string `yaml:"allow_methods"`
	AllowHeaders []string `yaml:"allow_headers"`
}

// StorageConfig ストレージ設定
type StorageConfig struct {
	SessionsDir string `yaml:"sessions_dir"`
	NotesPath   string `yaml:"notes_path"`
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
