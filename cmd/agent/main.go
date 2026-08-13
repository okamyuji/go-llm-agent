package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/enricher"
	"github.com/okamyuji/go-llm-agent/internal/eval"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/anthropic"
	"github.com/okamyuji/go-llm-agent/internal/llm/gemini"
	"github.com/okamyuji/go-llm-agent/internal/llm/llamacpp"
	"github.com/okamyuji/go-llm-agent/internal/llm/ollama"
	"github.com/okamyuji/go-llm-agent/internal/llm/openai"
	"github.com/okamyuji/go-llm-agent/internal/llm/retry"
	"github.com/okamyuji/go-llm-agent/internal/memory"
	"github.com/okamyuji/go-llm-agent/internal/obs"
	"github.com/okamyuji/go-llm-agent/internal/safety"
	"github.com/okamyuji/go-llm-agent/internal/secret"
	"github.com/okamyuji/go-llm-agent/internal/storage"
	"github.com/okamyuji/go-llm-agent/internal/tool"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
	"github.com/okamyuji/go-llm-agent/internal/transport/httpapi"
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
	opts, _, optsErr := agentOptions(cfg, tools, acc, filepath.Dir(*configPath))
	if optsErr != nil {
		return optsErr
	}
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
	// resolveAPIKey APIKeyEnv が未設定または resolver で解決失敗した場合に error を返す
	// 起動時に明示的に失敗を呼び出し側へ伝え、サイレント空キーでの実呼び出しを避ける
	resolveAPIKey := func(name string, envKeys ...string) (string, error) {
		key, usedEnv, err := secret.ResolveAny(resolver, envKeys...)
		if err != nil {
			return "", fmt.Errorf("provider %q: resolve api key from %v: %w", name, envKeys, err)
		}
		if key == "" {
			return "", fmt.Errorf("provider %q: api key from env %q resolved to empty value", name, usedEnv)
		}
		return key, nil
	}
	if pc, ok := cfg.Providers["openai"]; ok {
		key, err := resolveAPIKey("openai", pc.APIKeyEnv)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		provs["openai"] = wrapWithRetry("openai", openai.New(openai.Options{BaseURL: pc.BaseURL, APIKey: key, RequestTimeoutSeconds: pc.RequestTimeoutSeconds}), pc.Retry)
	}
	if pc, ok := cfg.Providers["anthropic"]; ok {
		key, err := resolveAPIKey("anthropic", pc.APIKeyEnv, "CLAUDE_API_KEY")
		if err != nil {
			return nil, nil, nil, nil, err
		}
		provs["anthropic"] = wrapWithRetry("anthropic", anthropic.New(anthropic.Options{BaseURL: pc.BaseURL, APIKey: key, RequestTimeoutSeconds: pc.RequestTimeoutSeconds}), pc.Retry)
	}
	if pc, ok := cfg.Providers["gemini"]; ok {
		key, err := resolveAPIKey("gemini", pc.APIKeyEnv)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		provs["gemini"] = wrapWithRetry("gemini", gemini.New(gemini.Options{BaseURL: pc.BaseURL, APIKey: key, RequestTimeoutSeconds: pc.RequestTimeoutSeconds}), pc.Retry)
	}
	if pc, ok := cfg.Providers["ollama"]; ok {
		provs["ollama"] = wrapWithRetry("ollama", ollama.New(ollama.Options{
			BaseURL:               pc.BaseURL,
			RequestTimeoutSeconds: pc.RequestTimeoutSeconds,
			Temperature:           pc.Temperature,
			Think:                 pc.Think,
		}), pc.Retry)
	}
	if pc, ok := cfg.Providers["llamacpp"]; ok {
		provs["llamacpp"] = wrapWithRetry("llamacpp", llamacpp.New(llamacpp.Options{
			BaseURL:               pc.BaseURL,
			RequestTimeoutSeconds: pc.RequestTimeoutSeconds,
			Temperature:           pc.Temperature,
			MaxTokens:             pc.MaxTokens,
			RepeatPenalty:         pc.RepeatPenalty,
			Think:                 pc.Think,
			ToolCallIDFormat:      pc.ToolCallIDFormat,
		}), pc.Retry)
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
	webFetch := tool.NewWebFetch(cfg.Tools.WebFetch, logger)
	if slices.Contains(cfg.Agent.EnabledTools, "web_fetch") {
		webFetch.WarnIfWebgrabMissing()
	}
	tools := []tool.Tool{
		tool.NewFSReadWithLogger(sb, cfg.Tools.FS.MaxReadBytes, logger),
		tool.NewFSWriteWithLogger(sb, logger),
		tool.NewShell(cfg.Tools.Shell, logger),
		tool.NewHTTPFetchWithLogger(cfg.Tools.HTTPFetch, logger),
		tool.NewSearchFiles(sb, cfg.Tools.SearchFiles),
		tool.NewWebSearch(cfg.Tools.WebSearch),
		webFetch,
	}
	notesPath := cfg.Storage.NotesPath
	if notesPath == "" {
		notesPath = filepath.Join(expand(cfg.Storage.SessionsDir), "notes.jsonl")
	} else {
		notesPath = expand(notesPath)
	}
	if ns, err := memory.NewFileNoteStore(notesPath); err == nil {
		tools = append(tools, &tool.NoteAddTool{Store: ns}, &tool.NoteSearchTool{Store: ns})
	} else {
		// strict_notes_init=true なら起動エラーで fast-fail
		// false (既定) では degraded mode で agent を継続させ、ツール 2 つが無効化される
		if cfg.Storage.StrictNotesInit {
			return nil, nil, nil, nil, fmt.Errorf("notes store init failed (strict_notes_init=true): %w", err)
		}
		logger.Error("degraded mode: notes store init failed; note_add and note_search are disabled but agent continues to start", "path", notesPath, "err", err)
	}
	toolReg := tool.NewRegistry(tools, cfg.Agent.EnabledTools)
	store := storage.NewSessionStore(expand(cfg.Storage.SessionsDir))
	return cfg, llmReg, toolReg, store, nil
}

// agentOptions config に基づき agent.Service のオプション集合を組み立てる
// safety / billing / strategy などの構築でエラーになった場合、呼び出し側に伝播する
// HTTPApprover を生成した場合、第 2 戻り値で返す。chat/run サブコマンドでは捨ててよい
func agentOptions(cfg *config.Config, tools tool.Registry, acc billing.Accumulator, configDir string) ([]agent.Option, *agent.HTTPApprover, error) {
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
	sc, err := buildScanner(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build safety scanner: %w", err)
	}
	if sc != nil {
		opts = append(opts, agent.WithScanner(sc))
	}
	rd, err := buildRedactor(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build safety redactor: %w", err)
	}
	if rd != nil {
		opts = append(opts, agent.WithRedactor(rd))
	}
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
		// default_decision と timeout_seconds は config.Load 側の validateApproval で
		// 既に検証済みのため、ここでは値をそのまま使う。fail-open 経路は存在しない
		approver = agent.NewHTTPApprover()
		timeout := time.Duration(cfg.Agent.Approval.TimeoutSeconds) * time.Second
		opts = append(opts, agent.WithApprover(approver, cfg.Agent.Approval.RequiredTools, timeout))
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

func cmdChat(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	model := fs.String("model", "", "model id (provider/name)")
	noSpinner := fs.Bool("no-spinner", false, "disable progress indicator and turn summary")
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
	opts, _, optsErr := agentOptions(cfg, tools, acc, filepath.Dir(*configPath))
	if optsErr != nil {
		return optsErr
	}
	svc := agent.New(reg, tools, opts...)
	// 入力履歴は rlwrap -H と同形式で永続化する (既存の ~/.agent_history を引き継ぐ)
	historyFile := ""
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		historyFile = filepath.Join(home, ".agent_history")
	}
	r := cliui.NewREPL(svc, cliui.Options{
		Model:          m,
		SystemPrompt:   cfg.Agent.SystemPrompt,
		MaxToolHops:    cfg.Agent.MaxToolHops,
		DisableSpinner: *noSpinner,
		HistoryFile:    historyFile,
	})
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
	opts, _, optsErr := agentOptions(cfg, tools, acc, filepath.Dir(*configPath))
	if optsErr != nil {
		return optsErr
	}
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
	opts, approver, optsErr := agentOptions(cfg, tools, acc, filepath.Dir(*configPath))
	if optsErr != nil {
		return optsErr
	}
	svc := agent.New(reg, tools, opts...)
	// serve のみ HTTPApprover を /v1/runs/<id>/approve に渡す
	// chat/run/eval は approver を保持せず必要に応じて捨てる
	// non-stream の最終 content に再適用する Redactor も渡す
	// (loop の chunk-by-chunk redact だけだと PII が chunk 境界を跨いで取りこぼされる)
	rd, err := buildRedactor(cfg)
	if err != nil {
		return fmt.Errorf("build safety redactor: %w", err)
	}
	return httpapi.ListenAndServe(ctx, a, svc, cfg, acc, approver, rd)
}

func cmdTools(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("tools", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// telemetry / 親 ctx の cancellation を伝搬させるため、サブコマンド受領 ctx をそのまま渡す
	_, _, tools, _, err := loadDeps(ctx, *configPath)
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
