package cliui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// Options REPL 生成オプション
type Options struct {
	Model        string
	SystemPrompt string
	MaxToolHops  int
	In           io.Reader
	Out          io.Writer
}

// REPL 対話型 CLI
type REPL struct {
	svc agent.Service
	opt Options
	in  io.Reader
	out io.Writer
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
	return &REPL{svc: svc, opt: opt, in: in, out: out}
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
		var finalContent strings.Builder
		for ev := range ch {
			switch ev.Kind {
			case agent.EventDelta:
				_, _ = io.WriteString(r.out, ev.Delta)
				finalContent.WriteString(ev.Delta)
			case agent.EventToolCall:
				fmt.Fprintf(r.out, "\n[tool_call %s]\n", ev.ToolCall.Name)
			case agent.EventToolResult:
				fmt.Fprintf(r.out, "[tool_result %s]\n", ev.ToolResult.Name)
			case agent.EventFinal:
				fmt.Fprintln(r.out)
			case agent.EventError:
				fmt.Fprintf(r.out, "\n[error] %v\n", ev.Err)
			}
		}
		history = append(history, llm.Message{Role: llm.RoleAssistant, Content: finalContent.String()})
	}
}
