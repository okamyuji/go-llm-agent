package cliui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// RunOneShot 1 回限りのプロンプト送信を行い結果を out に書き出す
func RunOneShot(ctx context.Context, svc agent.Service, model, systemPrompt, prompt string, maxHops int, out io.Writer) error {
	if out == nil {
		out = os.Stdout
	}
	if maxHops <= 0 {
		maxHops = 8
	}
	ch := make(chan agent.Event, 16)
	go func() {
		_ = svc.Run(ctx, agent.Input{
			Model:        model,
			SystemPrompt: systemPrompt,
			Messages:     []llm.Message{{Role: llm.RoleUser, Content: prompt}},
			MaxToolHops:  maxHops,
		}, ch)
		close(ch)
	}()
	var final strings.Builder
	for ev := range ch {
		switch ev.Kind {
		case agent.EventDelta:
			_, _ = io.WriteString(out, ev.Delta)
			final.WriteString(ev.Delta)
		case agent.EventToolCall:
			fmt.Fprintf(out, "\n[tool_call %s]\n", ev.ToolCall.Name)
		case agent.EventToolResult:
			fmt.Fprintf(out, "[tool_result %s]\n", ev.ToolResult.Name)
		case agent.EventError:
			return ev.Err
		}
	}
	fmt.Fprintln(out)
	return nil
}
