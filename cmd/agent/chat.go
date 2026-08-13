package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
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
	r := cliui.NewREPL(svc, cliui.Options{
		Model:            m,
		SystemPrompt:     cfg.Agent.SystemPrompt,
		MaxToolHops:      cfg.Agent.MaxToolHops,
		In:               p.In,
		Out:              p.Out,
		DisableSpinner:   p.NoSpinner,
		HistoryFile:      chatHistoryFile(),
		ApprovalPrompter: prompter,
	})
	return r.Run(ctx)
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
