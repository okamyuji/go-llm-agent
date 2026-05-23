package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/eval"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/anthropic"
	"github.com/okamyuji/go-llm-agent/internal/llm/gemini"
	"github.com/okamyuji/go-llm-agent/internal/llm/ollama"
	"github.com/okamyuji/go-llm-agent/internal/llm/openai"
	"github.com/okamyuji/go-llm-agent/internal/llm/retry"
	"github.com/okamyuji/go-llm-agent/internal/obs"
	"github.com/okamyuji/go-llm-agent/internal/safety"
	"github.com/okamyuji/go-llm-agent/internal/secret"
	"github.com/okamyuji/go-llm-agent/internal/storage"
	"github.com/okamyuji/go-llm-agent/internal/tool"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
	"github.com/okamyuji/go-llm-agent/internal/transport/httpapi"
)

var version = "dev"

// shutdownTelemetry main から呼ばれる shutdown フック。Init 失敗時は noop
var shutdownTelemetry obs.Shutdown = func(context.Context) error { return nil }

func main() {
	code := mainEntry()
	// shutdownTelemetry を必ず走らせるため os.Exit はここでのみ呼ぶ
	shutdownCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if err := shutdownTelemetry(shutdownCtx); err != nil {
		fmt.Fprintln(os.Stderr, "telemetry shutdown:", err)
	}
	if code != 0 {
		os.Exit(code)
	}
}

// mainEntry サブコマンド処理を内側関数に分離し、defer と shutdown を確実に実行する
func mainEntry() int {
	if len(os.Args) < 2 {
		usage()
		return 2
	}
	sub := os.Args[1]
	args := os.Args[2:]
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var err error
	switch sub {
	case "chat":
		err = cmdChat(ctx, args)
	case "run":
		err = cmdRun(ctx, args)
	case "serve":
		err = cmdServe(ctx, args)
	case "tools":
		err = cmdTools(args)
	case "config":
		err = cmdConfig(args)
	case "eval":
		err = cmdEval(ctx, args)
	case "version", "--version", "-v":
		fmt.Println(version)
		return 0
	default:
		usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: agent {chat|run|serve|tools|config|eval|version} [flags]")
}

// cmdEval suite ディレクトリの YAML を読み込み agent.Service で実行してレポートを書く
func cmdEval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	suite := fs.String("suite", "eval/cases", "directory containing *.yaml eval cases")
	report := fs.String("report", "eval/report.json", "output JSON report path")
	model := fs.String("model", "", "model id (provider/name) override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, reg, tools, _, err := loadDeps(ctx, *configPath)
	if err != nil {
		return err
	}
	m := *model
	if m == "" {
		m = cfg.DefaultModel
	}
	acc, err := buildBillingAccumulator(cfg)
	if err != nil {
		return err
	}
	opts := agentOptions(cfg, tools, acc)
	svc := agent.New(reg, tools, opts...)
	cases, err := eval.LoadSuite(*suite)
	if err != nil {
		return err
	}
	if len(cases) == 0 {
		return fmt.Errorf("no eval cases found under %s", *suite)
	}
	results, err := eval.RunSuite(ctx, svc, cases, m, cfg.Agent.MaxToolHops)
	if err != nil {
		return err
	}
	scores := make([]eval.Scores, len(cases))
	for i := range cases {
		scores[i] = eval.Score(cases[i], results[i])
	}
	if err := eval.WriteReport(*report, cases, results, scores); err != nil {
		return err
	}
	var passed int
	for _, s := range scores {
		if s.Passed {
			passed++
		}
	}
	fmt.Printf("eval report: %d/%d passed (report: %s)\n", passed, len(cases), *report)
	if passed != len(cases) {
		return fmt.Errorf("%d eval case(s) failed", len(cases)-passed)
	}
	return nil
}

func loadDeps(ctx context.Context, configPath string) (*config.Config, llm.Registry, tool.Registry, storage.SessionStore, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if cfg.Observability.OTel.Enabled {
		sd, terr := obs.InitTelemetry(ctx, obs.TelemetryConfig{
			Enabled:                cfg.Observability.OTel.Enabled,
			Endpoint:               cfg.Observability.OTel.Endpoint,
			Insecure:               cfg.Observability.OTel.Insecure,
			SampleRatio:            cfg.Observability.OTel.SampleRatio,
			ServiceName:            cfg.Observability.OTel.ServiceName,
			MetricsIntervalSeconds: cfg.Observability.OTel.MetricsIntervalSeconds,
		}, nil)
		if terr != nil {
			return nil, nil, nil, nil, fmt.Errorf("init telemetry: %w", terr)
		}
		shutdownTelemetry = sd
	}
	resolver := secret.NewResolver(".env")
	provs := map[string]llm.Provider{}
	if pc, ok := cfg.Providers["openai"]; ok {
		key, _ := resolver.Resolve(pc.APIKeyEnv)
		provs["openai"] = wrapWithRetry("openai", openai.New(openai.Options{BaseURL: pc.BaseURL, APIKey: key, RequestTimeoutSeconds: pc.RequestTimeoutSeconds}), pc.Retry)
	}
	if pc, ok := cfg.Providers["anthropic"]; ok {
		key, _ := resolver.Resolve(pc.APIKeyEnv)
		provs["anthropic"] = wrapWithRetry("anthropic", anthropic.New(anthropic.Options{BaseURL: pc.BaseURL, APIKey: key, RequestTimeoutSeconds: pc.RequestTimeoutSeconds}), pc.Retry)
	}
	if pc, ok := cfg.Providers["gemini"]; ok {
		key, _ := resolver.Resolve(pc.APIKeyEnv)
		provs["gemini"] = wrapWithRetry("gemini", gemini.New(gemini.Options{BaseURL: pc.BaseURL, APIKey: key, RequestTimeoutSeconds: pc.RequestTimeoutSeconds}), pc.Retry)
	}
	if pc, ok := cfg.Providers["ollama"]; ok {
		provs["ollama"] = wrapWithRetry("ollama", ollama.New(ollama.Options{BaseURL: pc.BaseURL, RequestTimeoutSeconds: pc.RequestTimeoutSeconds}), pc.Retry)
	}
	allowModels := map[string][]string{}
	fallbackMap := map[string]string{}
	for name, pc := range cfg.Providers {
		if len(pc.AllowModels) > 0 {
			allowModels[name] = pc.AllowModels
		}
		if pc.FallbackTo != "" {
			fallbackMap[name] = pc.FallbackTo
		}
	}
	llmReg := llm.NewRegistryWithFallback(provs, allowModels, fallbackMap)

	logger := obs.NewLogger(obs.LoggerOptions{Format: cfg.Logging.Format, Level: cfg.Logging.Level})
	sb := tool.NewSandboxWithDeny(cfg.Tools.FS.AllowPaths, cfg.Tools.FS.DenyPaths)
	tools := []tool.Tool{
		tool.NewFSReadWithLogger(sb, cfg.Tools.FS.MaxReadBytes, logger),
		tool.NewFSWriteWithLogger(sb, logger),
		tool.NewShell(cfg.Tools.Shell, logger),
		tool.NewHTTPFetchWithLogger(cfg.Tools.HTTPFetch, logger),
		tool.NewSearchFiles(sb, cfg.Tools.SearchFiles),
	}
	toolReg := tool.NewRegistry(tools, cfg.Agent.EnabledTools)
	store := storage.NewSessionStore(expand(cfg.Storage.SessionsDir))
	return cfg, llmReg, toolReg, store, nil
}

// agentOptions config に基づき agent.Service のオプション集合を組み立てる
func agentOptions(cfg *config.Config, tools tool.Registry, acc billing.Accumulator) []agent.Option {
	var opts []agent.Option
	if acc != nil {
		opts = append(opts, agent.WithBilling(acc))
	}
	if cfg.Agent.ToolValidation.Enabled {
		opts = append(opts, agent.WithValidator(agent.NewSchemaValidator(tools)))
		opts = append(opts, agent.WithDefaultValidationRetries(cfg.Agent.ToolValidation.MaxRetries))
	}
	if cfg.Agent.ToolChoice.Mode != "" {
		opts = append(opts, agent.WithDefaultToolChoice(&llm.ToolChoice{Mode: cfg.Agent.ToolChoice.Mode, Name: cfg.Agent.ToolChoice.Name}))
	}
	if sc, err := buildScanner(cfg); err == nil && sc != nil {
		opts = append(opts, agent.WithScanner(sc))
	}
	if rd, err := buildRedactor(cfg); err == nil && rd != nil {
		opts = append(opts, agent.WithRedactor(rd))
	}
	return opts
}

// buildScanner cfg.Safety.InputScanner から safety.Scanner を構築する
func buildScanner(cfg *config.Config) (safety.Scanner, error) {
	c := safety.InputScannerConfig{
		Enabled:      cfg.Safety.InputScanner.Enabled,
		BlockOnMatch: cfg.Safety.InputScanner.BlockOnMatch,
	}
	for _, p := range cfg.Safety.InputScanner.Patterns {
		c.Patterns = append(c.Patterns, safety.InputScannerRule{ID: p.ID, Regex: p.Regex})
	}
	return safety.NewScannerFromConfig(c)
}

// buildRedactor cfg.Safety.OutputRedactor から safety.Redactor を構築する
func buildRedactor(cfg *config.Config) (safety.Redactor, error) {
	c := safety.OutputRedactorConfig{Enabled: cfg.Safety.OutputRedactor.Enabled}
	for _, r := range cfg.Safety.OutputRedactor.Rules {
		c.Rules = append(c.Rules, safety.OutputRedactorRule{ID: r.ID, Regex: r.Regex, Replacement: r.Replacement})
	}
	return safety.NewRedactorFromConfig(c)
}

// wrapWithRetry RetryConfig を retry.Config に変換して Provider をラップする
// MaxAttempts <= 1 のとき WrapProvider は inner をそのまま返すため互換性が保たれる
func wrapWithRetry(name string, p llm.Provider, rc config.RetryConfig) llm.Provider {
	return retry.WrapProvider(name, p, retry.Config{
		MaxAttempts:    rc.MaxAttempts,
		InitialBackoff: time.Duration(rc.InitialBackoffMS) * time.Millisecond,
		MaxBackoff:     time.Duration(rc.MaxBackoffMS) * time.Millisecond,
		JitterRatio:    rc.JitterRatio,
	})
}

// buildBillingAccumulator config から billing.Accumulator を構築する
// pricing が一切設定されていない、かつ budget も無指定なら nil を返す（集計無効）
func buildBillingAccumulator(cfg *config.Config) (billing.Accumulator, error) {
	pricing := map[string]billing.Pricing{}
	for name, pc := range cfg.Providers {
		if pc.Pricing.InputPerMillionJPY != 0 || pc.Pricing.OutputPerMillionJPY != 0 {
			pricing[name] = billing.Pricing{
				InputPerMillionJPY:  pc.Pricing.InputPerMillionJPY,
				OutputPerMillionJPY: pc.Pricing.OutputPerMillionJPY,
			}
		}
	}
	budget := billing.Budget{
		SessionMaxTokens: cfg.Agent.Budget.SessionMaxTokens,
		DailyMaxCostJPY:  cfg.Agent.Budget.DailyMaxCostJPY,
	}
	if len(pricing) == 0 && budget.SessionMaxTokens == 0 && budget.DailyMaxCostJPY == 0 {
		return nil, nil
	}
	storePath := filepath.Join(expand(cfg.Storage.SessionsDir), "billing.jsonl")
	store, err := billing.NewFileStore(storePath)
	if err != nil {
		return nil, fmt.Errorf("billing store: %w", err)
	}
	return billing.NewAccumulator(billing.Config{Pricing: pricing, Budget: budget}, store), nil
}

func cmdChat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	model := fs.String("model", "", "model id (provider/name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, reg, tools, _, err := loadDeps(ctx, *configPath)
	if err != nil {
		return err
	}
	m := *model
	if m == "" {
		m = cfg.DefaultModel
	}
	acc, err := buildBillingAccumulator(cfg)
	if err != nil {
		return err
	}
	opts := agentOptions(cfg, tools, acc)
	svc := agent.New(reg, tools, opts...)
	r := cliui.NewREPL(svc, cliui.Options{Model: m, SystemPrompt: cfg.Agent.SystemPrompt, MaxToolHops: cfg.Agent.MaxToolHops})
	return r.Run(ctx)
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	model := fs.String("model", "", "model id (provider/name)")
	prompt := fs.String("p", "", "prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, reg, tools, _, err := loadDeps(ctx, *configPath)
	if err != nil {
		return err
	}
	m := *model
	if m == "" {
		m = cfg.DefaultModel
	}
	acc, err := buildBillingAccumulator(cfg)
	if err != nil {
		return err
	}
	opts := agentOptions(cfg, tools, acc)
	svc := agent.New(reg, tools, opts...)
	return cliui.RunOneShot(ctx, svc, m, cfg.Agent.SystemPrompt, *prompt, cfg.Agent.MaxToolHops, os.Stdout)
}

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	addr := fs.String("addr", "", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, reg, tools, _, err := loadDeps(ctx, *configPath)
	if err != nil {
		return err
	}
	a := *addr
	if a == "" {
		a = cfg.Server.Addr
	}
	acc, err := buildBillingAccumulator(cfg)
	if err != nil {
		return err
	}
	opts := agentOptions(cfg, tools, acc)
	svc := agent.New(reg, tools, opts...)
	return httpapi.ListenAndServe(ctx, a, svc, cfg, acc)
}

func cmdTools(args []string) error {
	fs := flag.NewFlagSet("tools", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, _, tools, _, err := loadDeps(context.Background(), *configPath)
	if err != nil {
		return err
	}
	for _, s := range tools.List() {
		fmt.Printf("- %s  %s\n", s.Name, s.Description)
	}
	return nil
}

func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	fmt.Printf("default_model: %s\n", cfg.DefaultModel)
	fmt.Printf("providers: %d 件\n", len(cfg.Providers))
	fmt.Printf("enabled_tools: %v\n", cfg.Agent.EnabledTools)
	return nil
}

func expand(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}
