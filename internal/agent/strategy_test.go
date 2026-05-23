package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func TestNewStrategy_DefaultsToReAct(t *testing.T) {
	t.Parallel()
	s, ok := NewStrategy("", "", "", 0, 0, 0, 0)
	if !ok || s.Name() != "react" {
		t.Fatalf("expected react, got name=%s ok=%v", s.Name(), ok)
	}
}

func TestNewStrategy_PlannerExecutor(t *testing.T) {
	t.Parallel()
	s, ok := NewStrategy("planner_executor", "p", "e", 0, 0, 0, 0)
	if !ok || s.Name() != "planner_executor" {
		t.Fatalf("expected planner_executor, got name=%s ok=%v", s.Name(), ok)
	}
}

func TestNewStrategy_Reflection(t *testing.T) {
	t.Parallel()
	s, ok := NewStrategy("reflection", "", "", 0, 2, 1, 4)
	if !ok || s.Name() != "reflection" {
		t.Fatalf("expected reflection, got name=%s ok=%v", s.Name(), ok)
	}
}

func TestNewStrategy_UnknownFallsBackToReAct(t *testing.T) {
	t.Parallel()
	s, ok := NewStrategy("does-not-exist", "", "", 0, 0, 0, 0)
	if ok {
		t.Error("ok must be false for unknown strategy")
	}
	if s.Name() != "react" {
		t.Errorf("expected react fallback, got %s", s.Name())
	}
}

// strategyEmptyTools strategy_test 内専用の空 tool.Registry
type strategyEmptyTools struct{}

func (strategyEmptyTools) Lookup(string) (tool.Tool, bool) { return nil, false }
func (strategyEmptyTools) List() []tool.Spec               { return nil }

// fakePromptCapturingRegistry Strategy.Run が SystemPrompt を改変したかを
// プロバイダー Stream への到達直前に観測するためのスタブ
type fakePromptCapturingRegistry struct {
	captured string
}

func (r *fakePromptCapturingRegistry) Resolve(model string) (llm.Provider, string, error) {
	return &fakePromptCapturingProvider{onSend: func(p string) { r.captured = p }}, model, nil
}
func (r *fakePromptCapturingRegistry) ResolveWithFallback(model string) (llm.Provider, string, llm.Provider, string, error) {
	return &fakePromptCapturingProvider{onSend: func(p string) { r.captured = p }}, model, nil, "", nil
}
func (r *fakePromptCapturingRegistry) List() []string { return []string{"fake"} }

type fakePromptCapturingProvider struct{ onSend func(string) }

func (p *fakePromptCapturingProvider) Name() string { return "fakeplan" }
func (p *fakePromptCapturingProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, nil
}
func (p *fakePromptCapturingProvider) Stream(_ context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			p.onSend(m.Content)
			break
		}
	}
	return emptyStreamForStrategy{}, nil
}

type emptyStreamForStrategy struct{}

func (emptyStreamForStrategy) Recv() (llm.StreamEvent, bool) { return llm.StreamEvent{}, false }
func (emptyStreamForStrategy) Close() error                  { return nil }

func TestPlannerExecutor_PromptInjectsPlannerHint(t *testing.T) {
	t.Parallel()
	reg := &fakePromptCapturingRegistry{}
	s := &service{reg: reg, tools: strategyEmptyTools{}}
	st := plannerExecutorStrategy{ExecutorModel: "fake/model-x"}
	out := make(chan Event, 8)
	go func() {
		defer close(out)
		_ = st.run(context.Background(), s, Input{
			Model:        "fake/model-x",
			SystemPrompt: "あなたはアシスタント",
			Messages:     []llm.Message{{Role: llm.RoleUser, Content: "do something"}},
			MaxToolHops:  0,
		}, out)
	}()
	for range out {
	}
	if !strings.Contains(reg.captured, "Planner") {
		t.Fatalf("planner hint must be injected into system prompt, got %q", reg.captured)
	}
}
