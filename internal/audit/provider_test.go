package audit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

type scriptedStream struct {
	events []llm.StreamEvent
	i      int
}

func (s *scriptedStream) Recv() (llm.StreamEvent, bool) {
	if s.i >= len(s.events) {
		return llm.StreamEvent{}, false
	}
	ev := s.events[s.i]
	s.i++
	return ev, true
}
func (s *scriptedStream) Close() error { return nil }

type scriptedProvider struct {
	events []llm.StreamEvent
	chat   *llm.ChatResponse
}

func (p *scriptedProvider) Name() string { return "fake" }
func (p *scriptedProvider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return p.chat, nil
}
func (p *scriptedProvider) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	return &scriptedStream{events: p.events}, nil
}

func TestWrapProviderNilEmitterReturnsSame(t *testing.T) {
	p := &scriptedProvider{}
	if WrapProvider(p, nil) != llm.Provider(p) {
		t.Fatal("nil emitter must return the same provider")
	}
}

func TestStreamRecordsRequestResponseUsageAndRedactsAcrossDeltas(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p", Redactor: upperRedactor{}})
	p := &scriptedProvider{events: []llm.StreamEvent{
		{DeltaText: "SEC"}, {DeltaText: "RET here"},
		{ToolCall: &llm.ToolCall{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"a":"SECRET"}`)}},
		{Usage: &llm.Usage{InputTokens: 3, OutputTokens: 4}, Finish: "tool_calls"},
	}}
	wp := WrapProvider(p, e)
	ctx := WithSessionID(context.Background(), "s")
	st, err := wp.Stream(ctx, llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok := st.Recv(); !ok {
			break
		}
	}
	_ = st.Close()
	_ = e.Shutdown(context.Background())
	evs := readAllEvents(t, dir)
	kinds := map[Kind]int{}
	for _, ev := range evs {
		kinds[ev.Kind]++
		if ev.Kind == KindLLMResponse {
			var p LLMResponsePayload
			_ = json.Unmarshal(ev.Payload, &p)
			if p.Content != "[R] here" {
				t.Errorf("chunk-boundary secret not redacted: %q", p.Content)
			}
			if p.ToolCall == nil || string(p.ToolCall.Arguments) != `{"a":"[R]"}` || ev.CallID != "c1" {
				t.Errorf("tool_call in response not recorded/redacted: %s", ev.Payload)
			}
			if p.Finish != "tool_calls" {
				t.Errorf("finish=%q", p.Finish)
			}
		}
	}
	if kinds[KindLLMRequest] != 1 || kinds[KindLLMResponse] != 1 || kinds[KindUsage] != 1 {
		t.Fatalf("kinds=%v", kinds)
	}
}

func TestChatRecordsAndEmitsUsageOnlyWhenNonZero(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p"})
	p := &scriptedProvider{chat: &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "sum"}, FinishReason: "stop"}}
	wp := WrapProvider(p, e)
	ctx := WithSessionID(context.Background(), "s")
	if _, err := wp.Chat(ctx, llm.ChatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	p.chat.Usage = llm.Usage{InputTokens: 1}
	if _, err := wp.Chat(ctx, llm.ChatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	_ = e.Shutdown(context.Background())
	evs := readAllEvents(t, dir)
	kinds := map[Kind]int{}
	for _, ev := range evs {
		kinds[ev.Kind]++
	}
	if kinds[KindLLMRequest] != 2 || kinds[KindLLMResponse] != 2 || kinds[KindUsage] != 1 {
		t.Fatalf("kinds=%v", kinds)
	}
}
