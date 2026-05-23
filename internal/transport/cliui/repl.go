package cliui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// Options REPL 生成オプション
type Options struct {
	Model          string
	SystemPrompt   string
	MaxToolHops    int
	In             io.Reader
	Out            io.Writer
	Spinner        *Spinner // nil なら DisableSpinner=false 時に自動生成
	DisableSpinner bool     // true なら turn サマリも出力しない（旧来動作）
}

// REPL 対話型 CLI
type REPL struct {
	svc agent.Service
	opt Options
	in  io.Reader
	out io.Writer
	sp  *Spinner
}

// NewREPL Service とオプションから REPL を生成する
func NewREPL(svc agent.Service, opt Options) *REPL {
	in := opt.In
	if in == nil {
		in = os.Stdin
	}
	out := opt.Out
	if out == nil {
		out = os.Stdout
	}
	if opt.MaxToolHops <= 0 {
		opt.MaxToolHops = 8
	}
	sp := opt.Spinner
	if sp == nil && !opt.DisableSpinner {
		sp = NewSpinner(SpinnerOptions{Out: out, Model: opt.Model})
	}
	return &REPL{svc: svc, opt: opt, in: in, out: out, sp: sp}
}

// Run REPL ループを実行する。/quit または EOF で終了
func (r *REPL) Run(ctx context.Context) error {
	sc := bufio.NewScanner(r.in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	history := []llm.Message{}
	fmt.Fprintln(r.out, "go-llm-agent REPL  /quit で終了")
	for {
		fmt.Fprint(r.out, ">> ")
		if !sc.Scan() {
			return sc.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == "/quit" || line == "/exit" {
			return nil
		}
		history = append(history, llm.Message{Role: llm.RoleUser, Content: line})
		ch := make(chan agent.Event, 16)
		go func(hist []llm.Message) {
			_ = r.svc.Run(ctx, agent.Input{
				Model:        r.opt.Model,
				SystemPrompt: r.opt.SystemPrompt,
				Messages:     hist,
				MaxToolHops:  r.opt.MaxToolHops,
			}, ch)
			close(ch)
		}(append([]llm.Message{}, history...))

		turnStart := time.Now()
		var toolCount, usageIn, usageOut int
		var finalContent strings.Builder
		r.startSpinner(PhaseThinking, "")
		for ev := range ch {
			switch ev.Kind {
			case agent.EventDelta:
				r.stopSpinner()
				_, _ = io.WriteString(r.out, ev.Delta)
				finalContent.WriteString(ev.Delta)
			case agent.EventToolCall:
				r.stopSpinner()
				name := ""
				if ev.ToolCall != nil {
					name = ev.ToolCall.Name
				}
				fmt.Fprintf(r.out, "\n[tool_call %s]\n", name)
				toolCount++
				r.startSpinner(PhaseTool, name)
			case agent.EventToolResult:
				r.stopSpinner()
				name := ""
				if ev.ToolResult != nil {
					name = ev.ToolResult.Name
				}
				fmt.Fprintf(r.out, "[tool_result %s]\n", name)
				r.startSpinner(PhaseThinking, "")
			case agent.EventUsage:
				if ev.Usage != nil {
					usageIn += ev.Usage.InputTokens
					usageOut += ev.Usage.OutputTokens
				}
			case agent.EventFinal:
				r.stopSpinner()
				fmt.Fprintln(r.out)
			case agent.EventError:
				r.stopSpinner()
				fmt.Fprintf(r.out, "\n[error] %v\n", ev.Err)
			}
		}
		r.stopSpinner()
		if !r.opt.DisableSpinner {
			fmt.Fprintf(r.out, "↳ done in %.1fs · %d tool · in %d / out %d tok\n",
				time.Since(turnStart).Seconds(), toolCount, usageIn, usageOut)
		}
		history = append(history, llm.Message{Role: llm.RoleAssistant, Content: finalContent.String()})
	}
}

// startSpinner sp が nil でなければスピナー描画を開始する
func (r *REPL) startSpinner(phase Phase, label string) {
	if r.sp == nil {
		return
	}
	r.sp.Start(phase, label)
}

// stopSpinner sp が nil でなければスピナー描画を停止する
func (r *REPL) stopSpinner() {
	if r.sp == nil {
		return
	}
	r.sp.Stop()
}
