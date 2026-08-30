package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// chatSessionParams chat サブコマンドの実行パラメータ。
// In / Out は既定 (nil) で os.Stdin / os.Stdout になる
type chatSessionParams struct {
	ConfigPath string
	Model      string
	NoSpinner  bool
	Resume     bool
	In         io.Reader
	Out        io.Writer
}

// runChatSession 設定を読み REPL を起動する。flag の解釈は呼び出し元が済ませる
func runChatSession(ctx context.Context, p chatSessionParams) error {
	cfg, reg, tools, _, err := loadDeps(ctx, p.ConfigPath, false)
	if err != nil {
		return err
	}
	m := p.Model
	if m == "" {
		m = cfg.DefaultModel
	}
	acc, err := buildBillingAccumulator(cfg)
	if err != nil {
		return err
	}
	// 承認が必要な構成のときだけ対話プロンプタを作り、agent と REPL の両方へ渡す
	prompter, decider := chatApprovalWiring(cfg)
	opts, _, optsErr := agentOptionsWithDecider(cfg, tools, acc, filepath.Dir(p.ConfigPath), decider)
	if optsErr != nil {
		return optsErr
	}
	svc := agent.New(reg, tools, opts...)

	outW := p.Out
	if outW == nil {
		outW = os.Stdout
	}
	chatDir := chatSessionsDir(cfg)
	sessionID, initialHistory, rerr := cliui.ResumeLatestSession(chatDir, p.Resume,
		func(msg string) { fmt.Fprintln(outW, msg) },
		func(msg string) { fmt.Fprintln(os.Stderr, msg) })
	if rerr != nil {
		return rerr
	}

	sysPrompt, agentsMDPaths, mdErr := resolveInstructions(cfg)
	if mdErr != nil {
		return mdErr
	}
	agentsMDPath := strings.Join(agentsMDPaths, ", ")
	memStore, memSection := resolveMemory(cfg, chatLogger(cfg))
	sysPrompt += memSection
	_ = memStore

	r := cliui.NewREPL(svc, cliui.Options{
		Model:            m,
		SystemPrompt:     sysPrompt,
		MaxToolHops:      cfg.Agent.MaxToolHops,
		In:               p.In,
		Out:              p.Out,
		DisableSpinner:   p.NoSpinner,
		HistoryFile:      chatHistoryFile(),
		ApprovalPrompter: prompter,
		Registry:         reg,
		Compaction: cliui.CompactionOptions{
			// Load 通過後は非 nil が保証される (00-overview 3.4)
			Enabled:             *cfg.Agent.Compaction.Enabled,
			ContextWindowTokens: cfg.Agent.Compaction.ContextWindowTokens,
			TriggerRatio:        cfg.Agent.Compaction.TriggerRatio,
			KeepRecentTurns:     cfg.Agent.Compaction.KeepRecentTurns,
		},
		SessionsDir:     chatDir,
		SessionID:       sessionID,
		InitialHistory:  initialHistory,
		AgentsMDPath:    agentsMDPath,
		AvailableModels: availableModels(cfg),
		Billing:         acc,
	})
	return r.Run(ctx)
}

// availableModels /model の一覧表示用に、プロバイダー名 → allow_models の
// 対応を作る。表示側の書き換えが config へ波及しないようスライスは複製する
func availableModels(cfg *config.Config) map[string][]string {
	avail := make(map[string][]string, len(cfg.Providers))
	for name, pc := range cfg.Providers {
		if pc.AllowModels == nil {
			avail[name] = nil
			continue
		}
		avail[name] = append([]string{}, pc.AllowModels...)
	}
	return avail
}

// chatSessionsDir cfg から chat セッションの保存先を解決する。
// expand は main パッケージのヘルパーのため、パス解決の本体は
// cliui.ChatSessionsDir に置き、ここは expand を掛けて渡すだけにする
func chatSessionsDir(cfg *config.Config) string {
	return cliui.ChatSessionsDir(expand(cfg.Storage.ChatSessionsDir), expand(cfg.Storage.SessionsDir))
}

// chatApprovalWiring 承認が必要な構成のときだけ対話プロンプタを生成する。
// 生成した prompter は REPL へ、同じ値を ApprovalDecider として agent へ渡す
func chatApprovalWiring(cfg *config.Config) (*cliui.ApprovalPrompter, agent.ApprovalDecider) {
	if len(cfg.Agent.Approval.RequiredTools) == 0 {
		return nil, nil
	}
	p := cliui.NewApprovalPrompter()
	return p, p
}

// chatHistoryFile 入力履歴の永続化先。rlwrap -H と同形式で ~/.agent_history を引き継ぐ。
// ホームディレクトリが解決できない場合は空文字を返しセッション内のみの履歴にする
func chatHistoryFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".agent_history")
}

// approvalOption 承認オプションを組み立てる。decider が非 nil (chat の対話プロンプタ)
// ならそれを使い、nil なら新規 HTTPApprover を broker アダプタで包む (serve・run・eval)。
// 生成した HTTPApprover は serve が Submit 経路へ渡すため第 2 戻り値で返す
func approvalOption(cfg *config.Config, decider agent.ApprovalDecider) (agent.Option, *agent.HTTPApprover) {
	// default_decision と timeout_seconds は config.Load 側の validateApproval で
	// 既に検証済みのため、ここでは値をそのまま使う。fail-open 経路は存在しない
	var approver *agent.HTTPApprover
	d := decider
	if d == nil {
		approver = agent.NewHTTPApprover()
		d = agent.NewBrokerDecider(approver)
	}
	timeout := time.Duration(cfg.Agent.Approval.TimeoutSeconds) * time.Second
	return agent.WithApprovalDecider(d, cfg.Agent.Approval.RequiredTools, timeout), approver
}
