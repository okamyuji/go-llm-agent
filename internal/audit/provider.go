package audit

import (
	"context"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// WrapProvider LLM 入出力を監査イベントにする。retry.WrapProvider の外側に置き、リトライは 1 組として記録する
func WrapProvider(p llm.Provider, e *Emitter) llm.Provider {
	if e == nil {
		return p
	}
	return &auditProvider{inner: p, e: e}
}

type auditProvider struct {
	inner llm.Provider
	e     *Emitter
}

func (a *auditProvider) Name() string { return a.inner.Name() }

func (a *auditProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	a.e.LLMRequest(ctx, a.inner.Name(), req.Model, req)
	res, err := a.inner.Chat(ctx, req)
	if err != nil {
		a.e.LLMResponse(ctx, a.inner.Name(), req.Model, "", nil, "", err)
		return res, err
	}
	var call *llm.ToolCall
	if len(res.Message.ToolCalls) > 0 {
		call = &res.Message.ToolCalls[0]
	}
	a.e.LLMResponse(ctx, a.inner.Name(), req.Model, res.Message.Content, call, res.FinishReason, nil)
	if res.Usage.InputTokens+res.Usage.OutputTokens > 0 {
		a.e.Usage(ctx, a.inner.Name(), req.Model, res.Usage)
	}
	return res, nil
}

func (a *auditProvider) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	a.e.LLMRequest(ctx, a.inner.Name(), req.Model, req)
	st, err := a.inner.Stream(ctx, req)
	if err != nil {
		a.e.LLMResponse(ctx, a.inner.Name(), req.Model, "", nil, "", err)
		return st, err
	}
	return &auditStream{inner: st, ctx: ctx, e: a.e, provider: a.inner.Name(), model: req.Model}, nil
}

type auditStream struct {
	inner    llm.ChatStream
	ctx      context.Context
	e        *Emitter
	provider string
	model    string
	content  strings.Builder
	call     *llm.ToolCall
	finish   string
	done     bool
}

func (s *auditStream) Recv() (llm.StreamEvent, bool) {
	ev, ok := s.inner.Recv()
	if !ok {
		s.finishOnce(nil)
		return ev, ok
	}
	if ev.Err != nil {
		s.finishOnce(ev.Err)
		return ev, ok
	}
	s.content.WriteString(ev.DeltaText)
	if ev.ToolCall != nil {
		c := *ev.ToolCall
		s.call = &c
	} else if len(ev.ToolCalls) > 0 {
		c := ev.ToolCalls[0]
		s.call = &c
	}
	if ev.Finish != "" {
		s.finish = ev.Finish
	}
	if ev.Usage != nil {
		s.e.Usage(s.ctx, s.provider, s.model, *ev.Usage)
	}
	return ev, ok
}

// finishOnce llm_response を 1 回だけ出す。content は結合後の全文に Emitter が redactor を通す
func (s *auditStream) finishOnce(err error) {
	if s.done {
		return
	}
	s.done = true
	s.e.LLMResponse(s.ctx, s.provider, s.model, s.content.String(), s.call, s.finish, err)
}

func (s *auditStream) Close() error {
	s.finishOnce(nil)
	return s.inner.Close()
}
