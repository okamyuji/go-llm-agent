package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// Run LLM とツールを最大 MaxToolHops 回交互に呼ぶ
func (s *service) Run(ctx context.Context, in Input, out chan<- Event) error {
	prov, model, err := s.reg.Resolve(in.Model)
	if err != nil {
		out <- Event{Kind: EventError, Err: err}
		return err
	}
	msgs := append([]llm.Message{}, in.Messages...)
	if in.SystemPrompt != "" {
		msgs = append([]llm.Message{{Role: llm.RoleSystem, Content: in.SystemPrompt}}, msgs...)
	}
	tools := s.specs()

	for hop := 0; hop <= in.MaxToolHops; hop++ {
		stream, err := prov.Stream(ctx, llm.ChatRequest{Model: model, Messages: msgs, Tools: tools})
		if err != nil {
			out <- Event{Kind: EventError, Err: err}
			return err
		}
		var contentBuilder strings.Builder
		var pendingCall *llm.ToolCall
		for {
			ev, ok := stream.Recv()
			if !ok {
				break
			}
			if ev.Err != nil {
				_ = stream.Close()
				out <- Event{Kind: EventError, Err: ev.Err}
				return ev.Err
			}
			if ev.DeltaText != "" {
				contentBuilder.WriteString(ev.DeltaText)
				out <- Event{Kind: EventDelta, Delta: ev.DeltaText}
			}
			if ev.ToolCall != nil {
				pendingCall = ev.ToolCall
				out <- Event{Kind: EventToolCall, ToolCall: ev.ToolCall}
			}
		}
		assistantContent := contentBuilder.String()
		if err := stream.Close(); err != nil {
			out <- Event{Kind: EventError, Err: err}
			return err
		}

		assistant := llm.Message{Role: llm.RoleAssistant, Content: assistantContent}
		if pendingCall != nil {
			assistant.ToolCalls = []llm.ToolCall{*pendingCall}
		}
		msgs = append(msgs, assistant)

		if pendingCall == nil {
			final := assistant
			out <- Event{Kind: EventFinal, Final: &final}
			return nil
		}

		t, ok := s.tools.Lookup(pendingCall.Name)
		if !ok {
			err := fmt.Errorf("tool %q が見つかりません", pendingCall.Name)
			out <- Event{Kind: EventError, Err: err}
			return err
		}
		res, terr := t.Execute(ctx, pendingCall.Arguments)
		tr := &ToolResult{CallID: pendingCall.ID, Name: pendingCall.Name, Content: res.Content, IsError: terr != nil || res.IsError}
		if terr != nil {
			tr.Content = terr.Error()
		}
		out <- Event{Kind: EventToolResult, ToolResult: tr}
		msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: tr.Content, ToolCallID: pendingCall.ID, Name: pendingCall.Name})
	}
	err = fmt.Errorf("max tool hops を超えました (%d)", in.MaxToolHops)
	out <- Event{Kind: EventError, Err: err}
	return err
}

func (s *service) specs() []llm.ToolSpec {
	var out []llm.ToolSpec
	for _, sp := range s.tools.List() {
		out = append(out, llm.ToolSpec{Name: sp.Name, Description: sp.Description, Schema: sp.Schema})
	}
	return out
}
