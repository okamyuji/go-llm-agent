package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/obs"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// Run LLM とツールを最大 MaxToolHops 回交互に呼ぶ
func (s *service) Run(ctx context.Context, in Input, out chan<- Event) error {
	ctx, agentSpan := obs.StartAgentSpan(ctx, in.Model)
	defer agentSpan.End()

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
		llmCtx, llmSpan := obs.StartLLMSpan(ctx, prov.Name(), model)
		stream, err := prov.Stream(llmCtx, llm.ChatRequest{Model: model, Messages: msgs, Tools: tools})
		if err != nil {
			llmSpan.End()
			out <- Event{Kind: EventError, Err: err}
			return err
		}
		var contentBuilder strings.Builder
		var pendingCall *llm.ToolCall
		var lastUsage *llm.Usage
		for {
			ev, ok := stream.Recv()
			if !ok {
				break
			}
			if ev.Err != nil {
				_ = stream.Close()
				llmSpan.End()
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
			if ev.Usage != nil {
				lastUsage = ev.Usage
			}
		}
		assistantContent := contentBuilder.String()
		if err := stream.Close(); err != nil {
			llmSpan.End()
			out <- Event{Kind: EventError, Err: err}
			return err
		}
		if lastUsage != nil {
			obs.RecordTokens(llmCtx, prov.Name(), model, lastUsage.InputTokens, lastUsage.OutputTokens)
			ev := Event{Kind: EventUsage, Usage: lastUsage}
			if s.billing != nil {
				sessionID := in.SessionID
				if sessionID == "" {
					sessionID = "default"
				}
				snap, berr := s.billing.Add(ctx, sessionID, prov.Name(), model, lastUsage.InputTokens, lastUsage.OutputTokens)
				if berr != nil {
					llmSpan.End()
					if errors.Is(berr, billing.ErrBudgetExceeded) {
						out <- Event{Kind: EventError, Err: berr}
						return berr
					}
					out <- Event{Kind: EventError, Err: berr}
					return berr
				}
				snapCopy := snap
				ev.Cost = &snapCopy
			}
			out <- ev
		}
		llmSpan.End()

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
		execCtx := context.WithValue(ctx, tool.CorrelationKey(), pendingCall.ID)
		execCtx, toolSpan := obs.StartToolSpan(execCtx, pendingCall.Name, pendingCall.ID)
		start := time.Now()
		res, terr := t.Execute(execCtx, pendingCall.Arguments)
		ok2 := terr == nil && !res.IsError
		obs.RecordToolOutcome(execCtx, pendingCall.Name, ok2, time.Since(start))
		toolSpan.End()
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
