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
	"github.com/okamyuji/go-llm-agent/internal/enricher"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/retry"
	"github.com/okamyuji/go-llm-agent/internal/obs"
	"github.com/okamyuji/go-llm-agent/internal/safety"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

var version = "dev"

// shutdownTelemetry OTel shutdown フック。main から呼ぶ。Init 失敗時 noop
var shutdownTelemetry obs.Shutdown = func(context.Context) error { return nil }

func main() {
	// runWithShutdown 内で mainEntry を defer 付きで包むことで、
	// panic でも正常終了でも shutdownTelemetry が必ず走る
	// os.Exit は defer を実行しないため、main の最終ステップとしてのみ呼ぶ
	code := runWithShutdown()
	if code != 0 {
		os.Exit(code)
	}
}

// runWithShutdown mainEntry を内部関数で実行し、defer で telemetry shutdown を保証する
// panic でも defer は走るため、obs span/metric の取りこぼしを防ぐ
func runWithShutdown() int {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		if err := shutdownTelemetry(shutdownCtx); err != nil {
			fmt.Fprintln(os.Stderr, "telemetry shutdown:", err)
		}
	}()
	return mainEntry()
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
		err = cmdTools(ctx, args)
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

func cmdChat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	model := fs.String("model", "", "model id (provider/name)")
	noSpinner := fs.Bool("no-spinner", false, "disable progress indicator and turn summary")
	resume := fs.Bool("resume", false, "resume the most recent chat session")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runChatSession(ctx, chatSessionParams{
		ConfigPath: *configPath,
		Model:      *model,
		NoSpinner:  *noSpinner,
		Resume:     *resume,
	})
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	model := fs.String("model", "", "model id (provider/name)")
	prompt := fs.String("p", "", "prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runOneShot(ctx, oneShotParams{ConfigPath: *configPath, Model: *model, Prompt: *prompt})
}

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	addr := fs.String("addr", "", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runServer(ctx, serveParams{ConfigPath: *configPath, Addr: *addr})
}

func cmdTools(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tools", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return listTools(ctx, *configPath, os.Stdout)
}

func cmdEval(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	suite := fs.String("suite", "eval/cases", "directory containing *.yaml eval cases")
	report := fs.String("report", "eval/report.json", "output JSON report path")
	model := fs.String("model", "", "model id (provider/name) override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return runEvalSuite(ctx, evalParams{
		ConfigPath: *configPath,
		Suite:      *suite,
		Report:     *report,
		Model:      *model,
	})
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

// agentOptions config に基づき agent.Service のオプション集合を組み立てる
// safety / billing / strategy などの構築でエラーになった場合、呼び出し側に伝播する
// HTTPApprover を生成した場合、第 2 戻り値で返す。chat/run サブコマンドでは捨ててよい
func agentOptions(cfg *config.Config, tools tool.Registry, acc billing.Accumulator, configDir string) ([]agent.Option, *agent.HTTPApprover, error) {
	return agentOptionsWithDecider(cfg, tools, acc, configDir, nil)
}

// agentOptionsWithDecider agentOptions に承認 decider の注入を加えた形。
// chat は対話プロンプタを渡し、serve・run・eval は nil を渡して broker を使う
func agentOptionsWithDecider(cfg *config.Config, tools tool.Registry, acc billing.Accumulator, configDir string, decider agent.ApprovalDecider) ([]agent.Option, *agent.HTTPApprover, error) {
	var opts []agent.Option
	var approver *agent.HTTPApprover
	if acc != nil {
		opts = append(opts, agent.WithBilling(acc))
	}
	if cfg.Agent.ToolValidation.Enabled {
		v, err := agent.NewSchemaValidator(tools)
		if err != nil {
			return nil, nil, fmt.Errorf("build schema validator: %w", err)
		}
		opts = append(opts, agent.WithValidator(v))
		opts = append(opts, agent.WithDefaultValidationRetries(cfg.Agent.ToolValidation.MaxRetries))
	}
	if cfg.Agent.ToolChoice.Mode != "" {
		opts = append(opts, agent.WithDefaultToolChoice(&llm.ToolChoice{Mode: cfg.Agent.ToolChoice.Mode, Name: cfg.Agent.ToolChoice.Name}))
	}
	if cfg.Agent.ToolResultLimit.MaxChars > 0 {
		opts = append(opts, agent.WithToolResultLimit(cfg.Agent.ToolResultLimit.MaxChars))
	}
	safety, err := safetyOptions(cfg)
	if err != nil {
		return nil, nil, err
	}
	opts = append(opts, safety...)
	if cfg.Agent.Strategy != "" {
		if st, ok := agent.NewStrategy(
			cfg.Agent.Strategy,
			cfg.Agent.PlannerExecutor.PlannerModel,
			cfg.Agent.PlannerExecutor.ExecutorModel,
			cfg.Agent.PlannerExecutor.MaxSteps,
			cfg.Agent.Reflection.MaxIterations,
			cfg.Agent.Reflection.ConsecutiveFailures,
			cfg.Agent.Reflection.HopBudget,
		); ok {
			opts = append(opts, agent.WithStrategy(st))
		}
	}
	if len(cfg.Agent.Approval.RequiredTools) > 0 {
		var approvalOpt agent.Option
		approvalOpt, approver = approvalOption(cfg, decider)
		opts = append(opts, approvalOpt)
	}
	if hr := hookRunner(cfg.Hooks); hr != nil {
		opts = append(opts, agent.WithHooks(hr))
	}
	if e := enricher.New(enricher.Config{
		Enabled:    cfg.Agent.Enricher.Enabled,
		PromptsDir: cfg.Agent.Enricher.PromptsDir,
		Languages:  cfg.Agent.Enricher.Languages,
		Dynamic: enricher.DynamicConfig{
			Enabled:       cfg.Agent.Enricher.Dynamic.Enabled,
			MaxSections:   cfg.Agent.Enricher.Dynamic.MaxSections,
			MaxBytes:      cfg.Agent.Enricher.Dynamic.MaxBytes,
			CacheDir:      cfg.Agent.Enricher.Dynamic.CacheDir,
			CacheTTLHours: cfg.Agent.Enricher.Dynamic.CacheTTLHours,
			Sources:       cfg.Agent.Enricher.Dynamic.Sources,
		},
	}, configDir); e != nil {
		opts = append(opts, agent.WithContextEnricher(e))
	}
	return opts, approver, nil
}

// hookRunner hooks が 1 件も無ければ nil を返し、既存動作へ影響させない
func hookRunner(h config.HooksConfig) *agent.HookRunner {
	if len(h.PreToolUse) == 0 && len(h.PostToolUse) == 0 {
		return nil
	}
	return agent.NewHookRunner(toHookSpecs(h.PreToolUse), toHookSpecs(h.PostToolUse))
}

// toHookSpecs config の hook 定義を agent の実行仕様へ変換する
func toHookSpecs(cs []config.HookConfig) []agent.HookSpec {
	out := make([]agent.HookSpec, 0, len(cs))
	for _, c := range cs {
		out = append(out, agent.HookSpec{
			Matcher: c.Matcher,
			Command: c.Command,
			Timeout: time.Duration(c.TimeoutSeconds) * time.Second,
		})
	}
	return out
}

// safetyOptions 入力スキャナと出力リダクタのオプションを組み立てる
func safetyOptions(cfg *config.Config) ([]agent.Option, error) {
	var opts []agent.Option
	sc, err := buildScanner(cfg)
	if err != nil {
		return nil, fmt.Errorf("build safety scanner: %w", err)
	}
	if sc != nil {
		opts = append(opts, agent.WithScanner(sc))
	}
	rd, err := buildRedactor(cfg)
	if err != nil {
		return nil, fmt.Errorf("build safety redactor: %w", err)
	}
	if rd != nil {
		opts = append(opts, agent.WithRedactor(rd))
	}
	return opts, nil
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

// buildRedactor cfg.Safety.OutputRedactor + PIIRedactor から ChainRedactor を構築する
// 06 番の OutputRedactor を先に適用し、14 番の PIIRedactor を後段で適用する
func buildRedactor(cfg *config.Config) (safety.Redactor, error) {
	oc := safety.OutputRedactorConfig{Enabled: cfg.Safety.OutputRedactor.Enabled}
	for _, r := range cfg.Safety.OutputRedactor.Rules {
		oc.Rules = append(oc.Rules, safety.OutputRedactorRule{ID: r.ID, Regex: r.Regex, Replacement: r.Replacement})
	}
	out, err := safety.NewRedactorFromConfig(oc)
	if err != nil {
		return nil, err
	}
	pc := safety.PIIRedactorConfig{Enabled: cfg.Safety.PIIRedactor.Enabled}
	for _, r := range cfg.Safety.PIIRedactor.Rules {
		pc.Rules = append(pc.Rules, safety.PIIRule{ID: r.ID, Regex: r.Regex, Replacement: r.Replacement})
	}
	pii, err := safety.NewPIIRedactor(pc)
	if err != nil {
		return nil, err
	}
	return safety.ChainRedactor(out, pii), nil
}

// excludeTool tools から name を除いた新しいスライスを返す (元スライスは変更しない)
func excludeTool(tools []string, name string) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		if t != name {
			out = append(out, t)
		}
	}
	return out
}

// wrapWithRetry RetryConfig を retry.Config に変換して Provider をラップする
// MaxAttempts <= 1 のとき WrapProvider が inner をそのまま返すため互換性が保たれる
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

func expand(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}
