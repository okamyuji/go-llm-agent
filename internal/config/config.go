package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

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
// Temperature 非 nil のとき推論温度を固定する (0 で決定的出力、ベンチマーク再現性向上)
// Think 非 nil のとき thinking モードを制御する (Ollama の Qwen 系 reasoning モデル等。
// false で thinking 無効化し空応答・低速問題を回避する)
type ProviderConfig struct {
	BaseURL               string        `yaml:"base_url"`
	APIKeyEnv             string        `yaml:"api_key_env"`
	AllowModels           []string      `yaml:"allow_models"`
	Pricing               PricingConfig `yaml:"pricing"`
	RequestTimeoutSeconds int           `yaml:"request_timeout_seconds"`
	Retry                 RetryConfig   `yaml:"retry"`
	FallbackTo            string        `yaml:"fallback_to"`
	Temperature           *float64      `yaml:"temperature"`
	// MaxTokens は 1 応答の生成上限トークン数 (llamacpp)。暴走・長時間化の抑制に使う。
	MaxTokens *int `yaml:"max_tokens"`
	// RepeatPenalty は繰り返し抑制係数 (llamacpp)。量子化モデルの繰り返し暴走対策。
	RepeatPenalty *float64 `yaml:"repeat_penalty"`
	Think         *bool    `yaml:"think"`
	// ToolCallIDFormat はツール呼び出し ID の正規化方式。llamacpp プロバイダーでのみ参照する。
	// "alnum9" 指定時、tool_call_id を 9 文字英数字へ書き換える (Mistral-Nemo 系テンプレートが
	// 「9 文字英数字」を強制し、llama-server 生成の 32 文字 ID を 2 ターン目で 400 拒否する問題への対策)。
	// 空文字なら書き換えない (Qwen 等はこの制約を持たない)。
	ToolCallIDFormat string `yaml:"tool_call_id_format"`
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
	MaxToolHops      int                   `yaml:"max_tool_hops"`
	EnabledTools     []string              `yaml:"enabled_tools"`
	SystemPrompt     string                `yaml:"system_prompt"`
	SystemPromptFile string                `yaml:"system_prompt_file"`
	Budget           BudgetConfig          `yaml:"budget"`
	ToolChoice       ToolChoiceConfig      `yaml:"tool_choice"`
	ToolValidation   ToolValidationConfig  `yaml:"tool_validation"`
	Approval         ApprovalConfig        `yaml:"approval"`
	Strategy         string                `yaml:"strategy"`
	PlannerExecutor  PlannerExecutorConfig `yaml:"planner_executor"`
	Reflection       ReflectionConfig      `yaml:"reflection"`
	ParallelTools    ParallelToolsConfig   `yaml:"parallel_tools"`
	Enricher         EnricherConfig        `yaml:"enricher"`
	ToolResultLimit  ToolResultLimitConfig `yaml:"tool_result_limit"`
}

// ToolResultLimitConfig ツール結果を履歴へ積む際の切り詰め設定
type ToolResultLimitConfig struct {
	// MaxChars 上限文字数 (rune 数)。未指定 (0) は applyDefaults が既定値へ
	// 置換する。切り詰めを無効化する場合は -1 を指定する (00-overview 3.4)
	MaxChars int `yaml:"max_chars"`
}

// EnricherConfig コンテキスト拡充の設定
type EnricherConfig struct {
	Enabled    bool                  `yaml:"enabled"`
	PromptsDir string                `yaml:"prompts_dir"`
	Languages  map[string]string     `yaml:"languages"`
	Dynamic    EnricherDynamicConfig `yaml:"dynamic"`
}

// EnricherDynamicConfig 動的ドキュメント検索の設定。
// 検出された言語の公式ドキュメントをフェッチし、質問に関連する
// セクションだけをキーワードマッチで選択してコンテキストに注入する
type EnricherDynamicConfig struct {
	Enabled       bool                `yaml:"enabled"`
	MaxSections   int                 `yaml:"max_sections"`
	MaxBytes      int                 `yaml:"max_bytes"`
	CacheDir      string              `yaml:"cache_dir"`
	CacheTTLHours int                 `yaml:"cache_ttl_hours"`
	Sources       map[string][]string `yaml:"sources"`
}

// ParallelToolsConfig 並列ツール実行の設定
// require_approval ツールが含まれる場合、バリア方式で自動的に直列化する
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
// default_decision: "deny" を指定する (allow は廃止)
// required_tools: "shell" や "fs_write" など承認必須のツール名一覧
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

// BudgetConfig 予算上限の設定。0 を指定すると無制限として扱う
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
	WebSearch   WebSearchToolConfig `yaml:"web_search"`
	WebFetch    WebFetchToolConfig  `yaml:"web_fetch"`
}

// WebSearchToolConfig web_search の設定 (design/17)
type WebSearchToolConfig struct {
	Endpoint       string `yaml:"endpoint"`
	UserAgent      string `yaml:"user_agent"`
	MaxResults     int    `yaml:"max_results"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// WebFetchToolConfig web_fetch の設定 (design/17)
type WebFetchToolConfig struct {
	WebgrabPath    string   `yaml:"webgrab_path"`
	MaxChars       int      `yaml:"max_chars"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
	AllowDomains   []string `yaml:"allow_domains"`
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
// trusted_proxies に CIDR を指定すると、その経由からのリクエストでのみ
// X-Forwarded-For / X-Real-IP を信頼してクライアント IP を判定する
type ServerAllowlist struct {
	CIDRs          []string `yaml:"cidrs"`
	TrustedProxies []string `yaml:"trusted_proxies"`
}

// ServerCORS CORS ヘッダ設定
type ServerCORS struct {
	Enabled      bool     `yaml:"enabled"`
	AllowOrigins []string `yaml:"allow_origins"`
	AllowMethods []string `yaml:"allow_methods"`
	AllowHeaders []string `yaml:"allow_headers"`
}

// StorageConfig ストレージ設定
// StrictNotesInit true のとき memory.NewFileNoteStore 失敗を起動エラーに昇格させる
// 既定 (false) では degraded mode (note_add / note_search 無効) で agent を継続させる
type StorageConfig struct {
	SessionsDir     string `yaml:"sessions_dir"`
	NotesPath       string `yaml:"notes_path"`
	StrictNotesInit bool   `yaml:"strict_notes_init"`
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
	// 厳密モードで decode する
	// `server.auth` などセキュリティ critical なキーのタイポで、本来の制御が
	// 暗黙に無効化される事故 (例: `server.uath:` と書いて Bearer 認証が無効化される) を防ぐ
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config parse: %w", err)
	}
	applyDefaults(&cfg)
	if err := validateFallbackChains(cfg.Providers); err != nil {
		return nil, err
	}
	if err := validateProviders(cfg.Providers); err != nil {
		return nil, err
	}
	if err := validateApproval(cfg.Agent.Approval); err != nil {
		return nil, err
	}
	if err := validateWebTools(cfg.Tools); err != nil {
		return nil, err
	}
	if err := validateToolResultLimit(cfg.Agent.ToolResultLimit); err != nil {
		return nil, err
	}
	if err := resolveSystemPromptFile(&cfg, path); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// resolveSystemPromptFile system_prompt_file が指定されている場合、config ファイルからの
// 相対パスとしてファイルを読み込み SystemPrompt に設定する。
// system_prompt と system_prompt_file の両方が指定されている場合はエラーにする
func resolveSystemPromptFile(cfg *Config, configPath string) error {
	if cfg.Agent.SystemPromptFile == "" {
		return nil
	}
	if cfg.Agent.SystemPrompt != "" {
		return fmt.Errorf("config: system_prompt と system_prompt_file は同時に指定できません")
	}
	p := cfg.Agent.SystemPromptFile
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(configPath), p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("config: system_prompt_file read: %w", err)
	}
	cfg.Agent.SystemPrompt = strings.TrimSpace(string(b))
	return nil
}

// validateProviders 起動時にプロバイダー固有設定の妥当性を検査する。
// tool_call_id_format は llamacpp プロバイダーが解釈する値のみ許可し、タイポを
// 実行時 (2 ターン目の HTTP 400) でなく起動時に fail-fast させる。
func validateProviders(providers map[string]ProviderConfig) error {
	for name, pc := range providers {
		switch pc.ToolCallIDFormat {
		case "", "alnum9":
			// 空 (書き換えなし) と alnum9 のみ許可
		default:
			return fmt.Errorf("config: provider %q の tool_call_id_format は \"\" または \"alnum9\" のみサポート (got %q)", name, pc.ToolCallIDFormat)
		}
	}
	return nil
}

// validateApproval 起動時に approval 設定の妥当性を検査する
// default_decision: allow は fail-open のため廃止。timeout_seconds は required_tools 指定時に必須
func validateApproval(a ApprovalConfig) error {
	if a.DefaultDecision == "allow" {
		return fmt.Errorf("config: agent.approval.default_decision=allow は廃止されました。\"deny\" に修正してください")
	}
	if a.DefaultDecision != "" && a.DefaultDecision != "deny" {
		return fmt.Errorf("config: agent.approval.default_decision は \"deny\" のみサポート (got %q)", a.DefaultDecision)
	}
	if len(a.RequiredTools) > 0 && a.TimeoutSeconds <= 0 {
		return fmt.Errorf("config: agent.approval.timeout_seconds は required_tools 指定時に正の整数で必須 (got %d)", a.TimeoutSeconds)
	}
	return nil
}

// defaultToolResultLimitMaxChars agent.tool_result_limit.max_chars の実効既定値
// (00-overview 3.4 節が凍結)。max_chars <= context_window_tokens * trigger_ratio * 0.4 を満たす
const defaultToolResultLimitMaxChars = 8000

// applyDefaults decode 直後・各 validateXxx の前に 1 回呼び、yaml で明示されなかった
// キーへコード既定値を適用する (00-overview 3.4 節)。数値キーはゼロ値 (未指定) の
// ときだけ既定値を代入する。切り詰めを明示的に無効化したい利用者は -1 を指定する
func applyDefaults(cfg *Config) {
	if cfg.Agent.ToolResultLimit.MaxChars == 0 {
		cfg.Agent.ToolResultLimit.MaxChars = defaultToolResultLimitMaxChars
	}
}

// validateToolResultLimit 起動時に tool_result_limit 設定の妥当性を検査する。
// -1 は切り詰めの明示的な無効化として受理する。それより小さい値は設定ミスとして拒否する
func validateToolResultLimit(c ToolResultLimitConfig) error {
	if c.MaxChars < -1 {
		return fmt.Errorf("config: agent.tool_result_limit.max_chars は -1 (無効) または 0 以上 (got %d)", c.MaxChars)
	}
	return nil
}

// validateWebTools 起動時に web_search / web_fetch 設定の妥当性を検査する (design/17 §7)
func validateWebTools(t ToolsConfig) error {
	if v := t.WebSearch.MaxResults; v != 0 && (v < 1 || v > 10) {
		return fmt.Errorf("config: tools.web_search.max_results は 1..10 (got %d)", v)
	}
	if ep := t.WebSearch.Endpoint; ep != "" {
		u, err := url.Parse(ep)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return fmt.Errorf("config: tools.web_search.endpoint はホスト名を含む http/https の URL が必要 (got %q)", ep)
		}
	}
	if v := t.WebFetch.MaxChars; v != 0 && v < 100 {
		return fmt.Errorf("config: tools.web_fetch.max_chars は 100 以上 (got %d)", v)
	}
	return nil
}

// validateFallbackChains providers の fallback_to から有向グラフを作り、サイクルが存在しないことを確認する
// サイクル付きの設定はランタイムで無限ループの原因になるため起動時に拒否する
func validateFallbackChains(providers map[string]ProviderConfig) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(providers))
	var dfs func(name string, stack []string) error
	dfs = func(name string, stack []string) error {
		switch color[name] {
		case gray:
			return fmt.Errorf("config: provider fallback cycle detected: %s", strings.Join(append(stack, name), " -> "))
		case black:
			return nil
		}
		color[name] = gray
		next := providers[name].FallbackTo
		if next != "" {
			if _, ok := providers[next]; !ok {
				return fmt.Errorf("config: provider %q fallback_to references unknown provider %q", name, next)
			}
			if err := dfs(next, append(stack, name)); err != nil {
				return err
			}
		}
		color[name] = black
		return nil
	}
	for name := range providers {
		if err := dfs(name, nil); err != nil {
			return err
		}
	}
	return nil
}
