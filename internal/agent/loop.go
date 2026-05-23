package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/obs"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// defaultApprovalTimeout WithApprover で timeout 未指定 (0) のときの既定値
// 無期限待機 (apCtx = ctx の継承) は goroutine リークの原因になるため明示的なフォールバックを置く
const defaultApprovalTimeout = 5 * time.Minute

// Run Strategy に処理を委譲する。Strategy 未設定なら ReAct で動く
func (s *service) Run(ctx context.Context, in Input, out chan<- Event) error {
	if s.strategy != nil {
		return s.strategy.run(ctx, s, in, out)
	}
	return s.runReAct(ctx, in, out)
}

// runReAct LLM とツールを最大 MaxToolHops 回交互に呼ぶ ReAct スタイルのループ本体
func (s *service) runReAct(ctx context.Context, in Input, out chan<- Event) error {
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
	// 06 番設計書 入力スキャナを最初の LLM 呼び出し前にすべての user/system メッセージへ適用する
	// 検出された場合は EventError で早期リターンする (fail-closed)
	if s.scanner != nil {
		for _, m := range msgs {
			if m.Role != llm.RoleUser && m.Role != llm.RoleSystem {
				continue
			}
			findings := s.scanner.Scan(m.Content)
			if len(findings) > 0 {
				err := fmt.Errorf("input scanner blocked role=%s pattern=%s", m.Role, findings[0].PatternID)
				out <- Event{Kind: EventError, Err: err}
				return err
			}
		}
	}
	tools := s.specs()

	validationRetries := 0
	lastValidationCallID := ""
	maxValidationRetries := max(in.ValidationMaxRetries, 0)
	if maxValidationRetries == 0 {
		maxValidationRetries = max(s.defaultMaxRetries, 0)
	}
	tc := in.ToolChoice
	if tc == nil {
		tc = s.defaultToolChoice
	}
	for hop := 0; hop <= in.MaxToolHops; hop++ {
		llmCtx, llmSpan := obs.StartLLMSpan(ctx, prov.Name(), model)
		stream, err := prov.Stream(llmCtx, llm.ChatRequest{Model: model, Messages: msgs, Tools: tools, ToolChoice: tc})
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
				if cerr := stream.Close(); cerr != nil {
					slog.WarnContext(ctx, "llm stream close failed after recv error",
						"provider", prov.Name(), "model", model, "err", cerr)
				}
				llmSpan.End()
				out <- Event{Kind: EventError, Err: ev.Err}
				return ev.Err
			}
			if ev.DeltaText != "" {
				delta := ev.DeltaText
				if s.redactor != nil {
					delta = s.redactor.Redact(delta)
				}
				contentBuilder.WriteString(delta)
				out <- Event{Kind: EventDelta, Delta: delta}
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
					// ErrBudgetExceeded を含むすべての billing エラーは EventError として伝播する
					// 旧コードは ErrBudgetExceeded 分岐で同一処理を二重に書いていたため統合した
					llmSpan.End()
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
		if s.validator != nil {
			// 異なる ToolCall に切り替わった場合は budget を per-call で初期化する
			if pendingCall.ID != lastValidationCallID {
				validationRetries = 0
				lastValidationCallID = pendingCall.ID
			}
			if vok, vmsg := s.validator.Validate(pendingCall.Name, pendingCall.Arguments); !vok {
				if validationRetries < maxValidationRetries {
					validationRetries++
					tr := &ToolResult{CallID: pendingCall.ID, Name: pendingCall.Name, Content: "schema validation failed: " + vmsg + " — please correct the arguments to match the JSON schema and try again", IsError: true}
					out <- Event{Kind: EventToolResult, ToolResult: tr}
					msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: tr.Content, ToolCallID: pendingCall.ID, Name: pendingCall.Name})
					continue
				}
				err := fmt.Errorf("schema validation max retries exceeded: %s", vmsg)
				out <- Event{Kind: EventError, Err: err}
				return err
			}
		}
		if s.approver != nil && s.approvalRequired[pendingCall.Name] {
			runID := in.SessionID
			if runID == "" {
				runID = "default"
			}
			// approvalTimeout 未指定 (0) は無期限待機による goroutine leak を招くため
			// defaultApprovalTimeout (5 分) にフォールバックする
			timeout := s.approvalTimeout
			if timeout <= 0 {
				timeout = defaultApprovalTimeout
			}
			apCtx, apCancel := context.WithTimeout(ctx, timeout)
			d, aerr := s.approver.Request(apCtx, ApprovalRequest{
				RunID: runID, CallID: pendingCall.ID, ToolName: pendingCall.Name, Arguments: pendingCall.Arguments,
			})
			apCancel()
			if aerr != nil && !errors.Is(aerr, ErrApprovalTimeout) {
				out <- Event{Kind: EventError, Err: aerr}
				return aerr
			}
			if !d.Allowed {
				tr := &ToolResult{CallID: pendingCall.ID, Name: pendingCall.Name, Content: "tool execution denied by reviewer: " + d.Reason, IsError: true}
				out <- Event{Kind: EventToolResult, ToolResult: tr}
				msgs = append(msgs, llm.Message{Role: llm.RoleTool, Content: tr.Content, ToolCallID: pendingCall.ID, Name: pendingCall.Name})
				continue
			}
		}
		execCtx := context.WithValue(ctx, tool.CorrelationKey(), pendingCall.ID)
		execCtx, toolSpan := obs.StartToolSpan(execCtx, pendingCall.Name, pendingCall.ID)
		start := time.Now()
		res, terr := t.Execute(execCtx, pendingCall.Arguments)
		ok2 := terr == nil && !res.IsError
		obs.RecordToolOutcome(execCtx, pendingCall.Name, ok2, time.Since(start))
		toolSpan.End()
		content := res.Content
		if terr != nil {
			content = terr.Error()
		}
		// 06 番設計書: 全ツール出力に untrusted マーカーを付与してプロンプトインジェクション耐性を高める
		if !strings.HasPrefix(content, "[UNTRUSTED INPUT") {
			content = "[UNTRUSTED INPUT: tool=" + pendingCall.Name + "]\n" + content + "\n[END UNTRUSTED]"
		}
		if s.redactor != nil {
			content = s.redactor.Redact(content)
		}
		tr := &ToolResult{CallID: pendingCall.ID, Name: pendingCall.Name, Content: content, IsError: terr != nil || res.IsError}
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
