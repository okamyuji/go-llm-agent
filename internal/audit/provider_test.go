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

// TestChatEmitsUsageForOutputTokensOnly は InputTokens=0, OutputTokens>0 の
// ケースを直接検証する。この組み合わせは
// InputTokens+OutputTokens>0 (真) と InputTokens-OutputTokens>0 (偽) で
// 判定が分かれるため、+ の取り違えを検出できる
// (TestChatRecordsAndEmitsUsageOnlyWhenNonZero は InputTokens 側のみで
// 両者が一致してしまい判別できない)。
func TestChatEmitsUsageForOutputTokensOnly(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p"})
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
	p := &scriptedProvider{chat: &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "sum"}, FinishReason: "stop"}}
	wp := WrapProvider(p, e)
	ctx := WithSessionID(context.Background(), "s")

	if _, err := wp.Chat(ctx, llm.ChatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if kinds := countKinds(readAllEvents(t, dir)); kinds[KindUsage] != 0 {
		t.Fatalf("zero usage must not emit a usage event, kinds=%v", kinds)
	}

	p.chat.Usage = llm.Usage{OutputTokens: 5}
	if _, err := wp.Chat(ctx, llm.ChatRequest{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if kinds := countKinds(readAllEvents(t, dir)); kinds[KindUsage] != 1 {
		t.Fatalf("output-tokens-only usage must emit exactly 1 usage event, kinds=%v", kinds)
	}
}

// TestStreamEmptyCompletionSatisfiesSchema Recv が即 (StreamEvent{}, false) を返す
// (デルタなし・tool_call なし・エラーなし) ケースでも llm_response の content が
// 空文字列のまま出力され、schema の anyOf (content|tool_call|error) を満たすことを検証する
func TestStreamEmptyCompletionSatisfiesSchema(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p"})
	p := &scriptedProvider{events: nil}
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
	s := loadSchema(t)
	var found bool
	for _, ev := range readAllEvents(t, dir) {
		if ev.Kind != KindLLMResponse {
			continue
		}
		found = true
		if verr := validate(t, s, ev); verr != nil {
			t.Errorf("llm_response with empty completion must satisfy schema: %v", verr)
		}
	}
	if !found {
		t.Fatal("llm_response event expected")
	}
}

func countKinds(evs []Event) map[Kind]int {
	kinds := map[Kind]int{}
	for _, ev := range evs {
		kinds[ev.Kind]++
	}
	return kinds
}
