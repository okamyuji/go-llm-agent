package agent_test

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/goleak"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// TestMain パッケージ全テスト終了時に goroutine リークを検証する
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type fakeStream struct {
	events []llm.StreamEvent
	i      int
}

func (f *fakeStream) Recv() (llm.StreamEvent, bool) {
	if f.i >= len(f.events) {
		return llm.StreamEvent{}, false
	}
	ev := f.events[f.i]
	f.i++
	return ev, true
}
func (f *fakeStream) Close() error { return nil }

type fakeProvider struct {
	streams  [][]llm.StreamEvent
	call     int
	requests []llm.ChatRequest
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, nil
}
func (f *fakeProvider) Stream(_ context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	f.requests = append(f.requests, req)
	ev := f.streams[f.call]
	f.call++
	return &fakeStream{events: ev}, nil
}

type fakeReg struct{ p llm.Provider }

func (f fakeReg) Resolve(model string) (llm.Provider, string, error) { return f.p, model, nil }
func (f fakeReg) ResolveWithFallback(model string) (llm.Provider, string, llm.Provider, string, error) {
	return f.p, model, nil, "", nil
}
func (f fakeReg) List() []string { return []string{"fake"} }

type echoTool struct{}

func (e echoTool) Spec() tool.Spec {
	return tool.Spec{Name: "echo", Description: "", Schema: json.RawMessage(`{}`)}
}
func (e echoTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: string(raw)}, nil
}

func TestRun_BasicToolLoop(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{
			{ToolCall: &llm.ToolCall{ID: "c1", Name: "echo", Arguments: json.RawMessage(`"ping"`)}},
		},
		{
			{DeltaText: "done"},
		},
	}}
	reg := fakeReg{p: prov}
	tools := tool.NewRegistry([]tool.Tool{echoTool{}}, []string{"echo"})
	svc := agent.New(reg, tools)

	out := make(chan agent.Event, 16)
	done := make(chan struct{})
	go func() {
		_ = svc.Run(context.Background(), agent.Input{
			Model:        "fake/m",
			Messages:     []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
			MaxToolHops:  3,
			EnabledTools: []string{"echo"},
		}, out)
		close(out)
		close(done)
	}()
	var kinds []agent.EventKind
	for ev := range out {
		kinds = append(kinds, ev.Kind)
	}
	<-done
	if len(kinds) < 3 {
		t.Fatalf("少なくとも 3 イベント期待, got %v", kinds)
	}
	if kinds[len(kinds)-1] != agent.EventFinal {
		t.Fatalf("最後は Final 期待 got %v", kinds[len(kinds)-1])
	}
}

func TestRun_ToolChoiceNoneSuppressesToolAdvertisement(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "answer"}},
	}}
	reg := fakeReg{p: prov}
	tools := tool.NewRegistry([]tool.Tool{echoTool{}}, []string{"echo"})
	svc := agent.New(reg, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 3,
		ToolChoice:  &llm.ToolChoice{Mode: "none"},
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(prov.requests))
	}
	if len(prov.requests[0].Tools) != 0 {
		t.Errorf("tool_choice none should suppress tool advertisement, got %d tools", len(prov.requests[0].Tools))
	}
	if prov.requests[0].ToolChoice != nil {
		t.Errorf("tool_choice should be nil when tools are suppressed, got %+v", prov.requests[0].ToolChoice)
	}
}

func TestRun_DefaultToolChoiceNoneSuppressesTools(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "answer"}},
	}}
	reg := fakeReg{p: prov}
	tools := tool.NewRegistry([]tool.Tool{echoTool{}}, []string{"echo"})
	svc := agent.New(reg, tools, agent.WithDefaultToolChoice(&llm.ToolChoice{Mode: "none"}))

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 3,
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if len(prov.requests[0].Tools) != 0 {
		t.Errorf("default tool_choice none should suppress tools, got %d tools", len(prov.requests[0].Tools))
	}
}

func TestRun_MaxHopsExceeded(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)}}},
		{{ToolCall: &llm.ToolCall{ID: "c2", Name: "echo", Arguments: json.RawMessage(`{}`)}}},
	}}
	reg := fakeReg{p: prov}
	tools := tool.NewRegistry([]tool.Tool{echoTool{}}, []string{"echo"})
	svc := agent.New(reg, tools)

	out := make(chan agent.Event, 64)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 0,
	}, out)
	close(out)
	if err == nil {
		t.Fatal("MaxToolHops 超過でエラー")
	}
}
