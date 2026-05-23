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
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/anthropic"
	"github.com/okamyuji/go-llm-agent/internal/llm/gemini"
	"github.com/okamyuji/go-llm-agent/internal/llm/ollama"
	"github.com/okamyuji/go-llm-agent/internal/llm/openai"
	"github.com/okamyuji/go-llm-agent/internal/obs"
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
	fmt.Fprintln(os.Stderr, "usage: agent {chat|run|serve|tools|config|version} [flags]")
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
		provs["openai"] = openai.New(openai.Options{BaseURL: pc.BaseURL, APIKey: key})
	}
	if pc, ok := cfg.Providers["anthropic"]; ok {
		key, _ := resolver.Resolve(pc.APIKeyEnv)
		provs["anthropic"] = anthropic.New(anthropic.Options{BaseURL: pc.BaseURL, APIKey: key})
	}
	if pc, ok := cfg.Providers["gemini"]; ok {
		key, _ := resolver.Resolve(pc.APIKeyEnv)
		provs["gemini"] = gemini.New(gemini.Options{BaseURL: pc.BaseURL, APIKey: key})
	}
	if pc, ok := cfg.Providers["ollama"]; ok {
		provs["ollama"] = ollama.New(ollama.Options{BaseURL: pc.BaseURL})
	}
	allowModels := map[string][]string{}
	for name, pc := range cfg.Providers {
		if len(pc.AllowModels) > 0 {
			allowModels[name] = pc.AllowModels
		}
	}
	llmReg := llm.NewRegistryWithAllowlist(provs, allowModels)

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
	svc := agent.New(reg, tools)
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
	svc := agent.New(reg, tools)
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
	svc := agent.New(reg, tools)
	return httpapi.ListenAndServe(ctx, a, svc, cfg)
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
