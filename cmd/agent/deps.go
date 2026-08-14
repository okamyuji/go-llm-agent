package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/anthropic"
	"github.com/okamyuji/go-llm-agent/internal/llm/gemini"
	"github.com/okamyuji/go-llm-agent/internal/llm/llamacpp"
	"github.com/okamyuji/go-llm-agent/internal/llm/ollama"
	"github.com/okamyuji/go-llm-agent/internal/llm/openai"
	"github.com/okamyuji/go-llm-agent/internal/memory"
	"github.com/okamyuji/go-llm-agent/internal/obs"
	"github.com/okamyuji/go-llm-agent/internal/secret"
	"github.com/okamyuji/go-llm-agent/internal/storage"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// loadDeps 設定・LLM registry・tool registry・session store を組み立てる。
// isServe が true のとき、enabled_tools に "fs_edit" が含まれていても tool registry
// から除外し、警告を stderr へ出す (03-fs-edit.md: read registry はプロセス単位で
// scope するため、複数リクエストが混在する serve では既読チェックが破綻する)
func loadDeps(ctx context.Context, configPath string, isServe bool) (*config.Config, llm.Registry, tool.Registry, storage.SessionStore, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if terr := initTelemetry(ctx, cfg); terr != nil {
		return nil, nil, nil, nil, terr
	}
	provs, perr := buildProviders(cfg, secret.NewResolver(".env"))
	if perr != nil {
		return nil, nil, nil, nil, perr
	}
	llmReg := buildLLMRegistry(cfg, provs)

	logger := obs.NewLogger(obs.LoggerOptions{Format: cfg.Logging.Format, Level: cfg.Logging.Level})
	sb := tool.NewSandboxWithDeny(cfg.Tools.FS.AllowPaths, cfg.Tools.FS.DenyPaths)
	readReg := tool.NewReadRegistry()
	tools, nerr := attachNoteTools(cfg, logger, buildToolList(cfg, logger, sb, readReg))
	if nerr != nil {
		return nil, nil, nil, nil, nerr
	}
	toolReg := tool.NewRegistry(tools, resolveEnabledTools(cfg, isServe))
	store := storage.NewSessionStore(expand(cfg.Storage.SessionsDir))
	return cfg, llmReg, toolReg, store, nil
}

// initTelemetry OTel が有効なときだけ初期化し、shutdown フックを差し替える。
// 無効時は何もせず nil を返し、既定の noop shutdown を保つ
func initTelemetry(ctx context.Context, cfg *config.Config) error {
	if !cfg.Observability.OTel.Enabled {
		return nil
	}
	sd, err := obs.InitTelemetry(ctx, obs.TelemetryConfig{
		Enabled:                cfg.Observability.OTel.Enabled,
		Endpoint:               cfg.Observability.OTel.Endpoint,
		Insecure:               cfg.Observability.OTel.Insecure,
		SampleRatio:            cfg.Observability.OTel.SampleRatio,
		ServiceName:            cfg.Observability.OTel.ServiceName,
		MetricsIntervalSeconds: cfg.Observability.OTel.MetricsIntervalSeconds,
	}, nil)
	if err != nil {
		return fmt.Errorf("init telemetry: %w", err)
	}
	shutdownTelemetry = sd
	return nil
}

// resolveProviderAPIKey APIKeyEnv が未設定または resolver で解決失敗した場合に error を返す。
// 起動時に明示的に失敗を呼び出し側へ伝え、サイレント空キーでの実呼び出しを避ける
func resolveProviderAPIKey(resolver secret.Resolver, name string, envKeys ...string) (string, error) {
	key, usedEnv, err := secret.ResolveAny(resolver, envKeys...)
	if err != nil {
		return "", fmt.Errorf("provider %q: resolve api key from %v: %w", name, envKeys, err)
	}
	if key == "" {
		return "", fmt.Errorf("provider %q: api key from env %q resolved to empty value", name, usedEnv)
	}
	return key, nil
}

// buildProviders config に定義されたプロバイダーだけを構築し、リトライでラップする。
// API キーを要するプロバイダーは起動時に解決し、失敗すれば error を返す
func buildProviders(cfg *config.Config, resolver secret.Resolver) (map[string]llm.Provider, error) {
	provs := map[string]llm.Provider{}
	if pc, ok := cfg.Providers["openai"]; ok {
		key, err := resolveProviderAPIKey(resolver, "openai", pc.APIKeyEnv)
		if err != nil {
			return nil, err
		}
		provs["openai"] = wrapWithRetry("openai", openai.New(openai.Options{BaseURL: pc.BaseURL, APIKey: key, RequestTimeoutSeconds: pc.RequestTimeoutSeconds}), pc.Retry)
	}
	if pc, ok := cfg.Providers["anthropic"]; ok {
		key, err := resolveProviderAPIKey(resolver, "anthropic", pc.APIKeyEnv, "CLAUDE_API_KEY")
		if err != nil {
			return nil, err
		}
		provs["anthropic"] = wrapWithRetry("anthropic", anthropic.New(anthropic.Options{BaseURL: pc.BaseURL, APIKey: key, RequestTimeoutSeconds: pc.RequestTimeoutSeconds}), pc.Retry)
	}
	if pc, ok := cfg.Providers["gemini"]; ok {
		key, err := resolveProviderAPIKey(resolver, "gemini", pc.APIKeyEnv)
		if err != nil {
			return nil, err
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
	return provs, nil
}

// buildLLMRegistry allow_models と fallback_to を config から取り出して registry を組む
func buildLLMRegistry(cfg *config.Config, provs map[string]llm.Provider) llm.Registry {
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
	return llm.NewRegistryWithFallback(provs, allowModels, fallbackMap)
}

// buildToolList 組み込みツールを構築する。有効化の絞り込みは registry 側が行うため、
// ここでは enabled_tools に関わらず全ツールを生成する
func buildToolList(cfg *config.Config, logger *slog.Logger, sb *tool.Sandbox, readReg *tool.ReadRegistry) []tool.Tool {
	webFetch := tool.NewWebFetch(cfg.Tools.WebFetch, logger)
	if slices.Contains(cfg.Agent.EnabledTools, "web_fetch") {
		webFetch.WarnIfWebgrabMissing()
	}
	return []tool.Tool{
		tool.NewFSReadWithLogger(sb, cfg.Tools.FS.MaxReadBytes, logger, readReg),
		tool.NewFSWriteWithLogger(sb, logger, readReg),
		tool.NewFSEdit(sb, readReg, logger),
		tool.NewShell(cfg.Tools.Shell, logger, cfg.Tools.FS.AllowPaths),
		tool.NewHTTPFetchWithLogger(cfg.Tools.HTTPFetch, logger),
		tool.NewSearchFiles(sb, cfg.Tools.SearchFiles),
		tool.NewWebSearch(cfg.Tools.WebSearch),
		webFetch,
	}
}

// attachNoteTools ノートストアを開いて note_add / note_search を追加する。
// strict_notes_init=true なら起動エラーで fast-fail し、false (既定) では
// degraded mode としてツール 2 つを無効化したまま agent を継続させる
func attachNoteTools(cfg *config.Config, logger *slog.Logger, tools []tool.Tool) ([]tool.Tool, error) {
	notesPath := cfg.Storage.NotesPath
	if notesPath == "" {
		notesPath = filepath.Join(expand(cfg.Storage.SessionsDir), "notes.jsonl")
	} else {
		notesPath = expand(notesPath)
	}
	ns, err := memory.NewFileNoteStore(notesPath)
	if err == nil {
		return append(tools, &tool.NoteAddTool{Store: ns}, &tool.NoteSearchTool{Store: ns}), nil
	}
	if cfg.Storage.StrictNotesInit {
		return nil, fmt.Errorf("notes store init failed (strict_notes_init=true): %w", err)
	}
	logger.Error("degraded mode: notes store init failed; note_add and note_search are disabled but agent continues to start", "path", notesPath, "err", err)
	return tools, nil
}

// resolveEnabledTools serve では fs_edit を除外する。read registry はプロセス単位の
// scope のため、複数リクエストが混在する serve では既読チェックが破綻する
func resolveEnabledTools(cfg *config.Config, isServe bool) []string {
	enabled := cfg.Agent.EnabledTools
	if isServe && slices.Contains(enabled, "fs_edit") {
		fmt.Fprintln(os.Stderr, "warn: serve では fs_edit を無効化します (read registry はプロセス単位のため複数リクエストで既読チェックが破綻する)")
		return excludeTool(enabled, "fs_edit")
	}
	return enabled
}

// serviceDeps run / serve / eval / tools が共通で必要とする組み立て済み依存
type serviceDeps struct {
	cfg      *config.Config
	svc      agent.Service
	acc      billing.Accumulator
	approver *agent.HTTPApprover
	model    string
}

// buildServiceDeps config の読み込みから agent.Service の生成までをまとめる。
// modelOverride が空なら config の default_model を使う
func buildServiceDeps(ctx context.Context, configPath, modelOverride string, isServe bool) (*serviceDeps, error) {
	cfg, reg, tools, _, err := loadDeps(ctx, configPath, isServe)
	if err != nil {
		return nil, err
	}
	acc, accErr := buildBillingAccumulator(cfg)
	if accErr != nil {
		return nil, accErr
	}
	opts, approver, optsErr := agentOptions(cfg, tools, acc, filepath.Dir(configPath))
	if optsErr != nil {
		return nil, optsErr
	}
	return &serviceDeps{
		cfg:      cfg,
		svc:      agent.New(reg, tools, opts...),
		acc:      acc,
		approver: approver,
		model:    resolveModel(cfg, modelOverride),
	}, nil
}

// resolveModel フラグ指定を優先し、未指定なら config の default_model を使う
func resolveModel(cfg *config.Config, override string) string {
	if override != "" {
		return override
	}
	return cfg.DefaultModel
}
