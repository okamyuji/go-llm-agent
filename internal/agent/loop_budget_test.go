package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// budgetStream Usage を最後に流す単一ストリーム
type budgetStream struct {
	events []llm.StreamEvent
	idx    int
}

func (b *budgetStream) Recv() (llm.StreamEvent, bool) {
	if b.idx >= len(b.events) {
		return llm.StreamEvent{}, false
	}
	ev := b.events[b.idx]
	b.idx++
	return ev, true
}

func (b *budgetStream) Close() error { return nil }

// budgetProvider Usage を確実に返す fake provider
type budgetProvider struct {
	usage llm.Usage
}

func (p *budgetProvider) Name() string { return "fakebill" }

func (p *budgetProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, Usage: p.usage}, nil
}

func (p *budgetProvider) Stream(_ context.Context, _ llm.ChatRequest) (llm.ChatStream, error) {
	usage := p.usage
	return &budgetStream{events: []llm.StreamEvent{
		{DeltaText: "ok"},
		{Usage: &usage},
	}}, nil
}

// budgetRegistry 単一 budgetProvider を返す
type budgetRegistry struct{ p llm.Provider }

func (b *budgetRegistry) Resolve(_ string) (llm.Provider, string, error) {
	return b.p, "fakebill-model", nil
}
func (b *budgetRegistry) ResolveWithFallback(_ string) (llm.Provider, string, llm.Provider, string, error) {
	return b.p, "fakebill-model", nil, "", nil
}
func (b *budgetRegistry) List() []string { return []string{"fakebill/fakebill-model"} }

// budgetTools 空のツール集合
type budgetTools struct{}

func (budgetTools) Lookup(string) (tool.Tool, bool) { return nil, false }
func (budgetTools) List() []tool.Spec               { return nil }

// budgetMemoryStore Accumulator 用のインメモリ Store
type budgetMemoryStore struct{ items []billing.Snapshot }

func (m *budgetMemoryStore) Append(_ context.Context, s billing.Snapshot) error {
	m.items = append(m.items, s)
	return nil
}
func (m *budgetMemoryStore) QuerySession(_ context.Context, id string) ([]billing.Snapshot, error) {
	var out []billing.Snapshot
	for _, s := range m.items {
		if s.SessionID == id {
			out = append(out, s)
		}
	}
	return out, nil
}
func (m *budgetMemoryStore) QueryDate(context.Context, string) ([]billing.Snapshot, error) {
	return m.items, nil
}

func TestRun_EmitsUsageAndCostWhenBillingEnabled(t *testing.T) {
	t.Parallel()

	prov := &budgetProvider{usage: llm.Usage{InputTokens: 1000, OutputTokens: 500}}
	pricing := map[string]billing.Pricing{
		"fakebill": {InputPerMillionJPY: 1000, OutputPerMillionJPY: 2000},
	}
	acc := billing.NewAccumulator(billing.Config{Pricing: pricing}, &budgetMemoryStore{})

	svc := agent.New(&budgetRegistry{p: prov}, budgetTools{}, agent.WithBilling(acc))
	out := make(chan agent.Event, 16)
	in := agent.Input{Model: "fakebill/fakebill-model", SessionID: "sess-1", MaxToolHops: 0, Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "ping"},
	}}

	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		errCh <- svc.Run(context.Background(), in, out)
	}()

	var sawUsage, sawCost, sawFinal bool
	for ev := range out {
		switch ev.Kind {
		case agent.EventUsage:
			if ev.Usage != nil && ev.Usage.InputTokens == 1000 {
				sawUsage = true
			}
			if ev.Cost != nil && ev.Cost.SessionID == "sess-1" {
				sawCost = true
			}
		case agent.EventFinal:
			sawFinal = true
		}
	}
	if !sawUsage {
		t.Fatal("EventUsage was not received or Usage missing")
	}
	if !sawCost {
		t.Fatal("EventUsage did not contain Cost snapshot")
	}
	if !sawFinal {
		t.Fatal("EventFinal was not received")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("svc.Run returned unexpected err: %v", err)
	}
	if got := acc.SessionTotal("sess-1"); got.InputTokens != 1000 || got.OutputTokens != 500 {
		t.Fatalf("SessionTotal unexpected: %+v", got)
	}
}

func TestRun_StopsOnBudgetExceeded(t *testing.T) {
	t.Parallel()

	prov := &budgetProvider{usage: llm.Usage{InputTokens: 10_000, OutputTokens: 0}}
	pricing := map[string]billing.Pricing{
		"fakebill": {InputPerMillionJPY: 1, OutputPerMillionJPY: 1},
	}
	acc := billing.NewAccumulator(billing.Config{
		Pricing: pricing,
		Budget:  billing.Budget{SessionMaxTokens: 5_000},
	}, &budgetMemoryStore{})

	svc := agent.New(&budgetRegistry{p: prov}, budgetTools{}, agent.WithBilling(acc))
	out := make(chan agent.Event, 16)
	in := agent.Input{Model: "fakebill/fakebill-model", SessionID: "sess-1", MaxToolHops: 0, Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "ping"},
	}}

	errCh := make(chan error, 1)
	go func() {
		defer close(out)
		errCh <- svc.Run(context.Background(), in, out)
	}()

	for range out {
	}
	if err := <-errCh; !errors.Is(err, billing.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
}
