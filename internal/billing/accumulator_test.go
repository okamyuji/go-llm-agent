package billing

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAccumulator_AddComputesCostJPY(t *testing.T) {
	t.Parallel()

	pricing := map[string]Pricing{
		"openai": {InputPerMillionJPY: 450, OutputPerMillionJPY: 1800},
	}
	store := newMemoryStore()
	acc := NewAccumulator(Config{Pricing: pricing, Now: fixedNow("2026-05-23")}, store)

	snap, err := acc.Add(context.Background(), "sess-1", "openai", "gpt-4.1-mini", 1_000_000, 500_000)
	if err != nil {
		t.Fatalf("Add returned unexpected error: %v", err)
	}

	if snap.SessionID != "sess-1" {
		t.Errorf("SessionID = %q want sess-1", snap.SessionID)
	}
	if snap.Provider != "openai" || snap.Model != "gpt-4.1-mini" {
		t.Errorf("Provider/Model unexpected: %v %v", snap.Provider, snap.Model)
	}
	if snap.InputTokens != 1_000_000 || snap.OutputTokens != 500_000 {
		t.Errorf("token counts unexpected: in=%d out=%d", snap.InputTokens, snap.OutputTokens)
	}
	want := 450.0 + 900.0
	if snap.CostJPY != want {
		t.Errorf("CostJPY = %v want %v", snap.CostJPY, want)
	}
}

func TestAccumulator_AddZeroPricingMeansZeroCost(t *testing.T) {
	t.Parallel()

	store := newMemoryStore()
	acc := NewAccumulator(Config{Pricing: nil, Now: fixedNow("2026-05-23")}, store)

	snap, err := acc.Add(context.Background(), "sess-1", "ollama", "llama3", 1_000, 1_000)
	if err != nil {
		t.Fatalf("Add returned unexpected error: %v", err)
	}
	if snap.CostJPY != 0 {
		t.Errorf("CostJPY = %v want 0 when pricing is missing", snap.CostJPY)
	}
}

func TestAccumulator_SessionTotalAccumulates(t *testing.T) {
	t.Parallel()

	pricing := map[string]Pricing{
		"openai": {InputPerMillionJPY: 1000, OutputPerMillionJPY: 2000},
	}
	store := newMemoryStore()
	acc := NewAccumulator(Config{Pricing: pricing, Now: fixedNow("2026-05-23")}, store)

	ctx := context.Background()
	if _, err := acc.Add(ctx, "sess-1", "openai", "gpt", 1_000_000, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.Add(ctx, "sess-1", "openai", "gpt", 0, 1_000_000); err != nil {
		t.Fatal(err)
	}
	total := acc.SessionTotal("sess-1")
	if total.InputTokens != 1_000_000 || total.OutputTokens != 1_000_000 {
		t.Errorf("token totals unexpected: %+v", total)
	}
	if total.CostJPY != 3000 {
		t.Errorf("CostJPY total = %v want 3000", total.CostJPY)
	}
}

func TestAccumulator_DailyTotalAggregatesAcrossSessions(t *testing.T) {
	t.Parallel()

	pricing := map[string]Pricing{
		"openai": {InputPerMillionJPY: 500, OutputPerMillionJPY: 500},
	}
	store := newMemoryStore()
	acc := NewAccumulator(Config{Pricing: pricing, Now: fixedNow("2026-05-23")}, store)

	ctx := context.Background()
	for _, sess := range []string{"a", "b", "c"} {
		if _, err := acc.Add(ctx, sess, "openai", "gpt", 1_000_000, 0); err != nil {
			t.Fatal(err)
		}
	}
	total := acc.DailyTotal("2026-05-23")
	if total.InputTokens != 3_000_000 {
		t.Errorf("input total %d want 3_000_000", total.InputTokens)
	}
	if total.CostJPY != 1500 {
		t.Errorf("daily CostJPY = %v want 1500", total.CostJPY)
	}
}

func TestAccumulator_BudgetSessionTokensExceeded(t *testing.T) {
	t.Parallel()

	pricing := map[string]Pricing{"openai": {InputPerMillionJPY: 1, OutputPerMillionJPY: 1}}
	store := newMemoryStore()
	acc := NewAccumulator(Config{
		Pricing: pricing,
		Budget:  Budget{SessionMaxTokens: 1500},
		Now:     fixedNow("2026-05-23"),
	}, store)

	ctx := context.Background()
	if _, err := acc.Add(ctx, "sess-1", "openai", "gpt", 1000, 0); err != nil {
		t.Fatalf("first add must succeed: %v", err)
	}
	_, err := acc.Add(ctx, "sess-1", "openai", "gpt", 600, 0)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
}

func TestAccumulator_BudgetDailyCostExceeded(t *testing.T) {
	t.Parallel()

	pricing := map[string]Pricing{"openai": {InputPerMillionJPY: 1000, OutputPerMillionJPY: 1000}}
	store := newMemoryStore()
	acc := NewAccumulator(Config{
		Pricing: pricing,
		Budget:  Budget{DailyMaxCostJPY: 1.0},
		Now:     fixedNow("2026-05-23"),
	}, store)

	ctx := context.Background()
	if _, err := acc.Add(ctx, "sess-1", "openai", "gpt", 500, 0); err != nil {
		t.Fatalf("first add must succeed within budget: %v", err)
	}
	_, err := acc.Add(ctx, "sess-2", "openai", "gpt", 1000, 0)
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded across sessions, got %v", err)
	}
}

func TestAccumulator_StoreFailurePropagates(t *testing.T) {
	t.Parallel()

	pricing := map[string]Pricing{"openai": {InputPerMillionJPY: 1, OutputPerMillionJPY: 1}}
	store := &failingStore{}
	acc := NewAccumulator(Config{Pricing: pricing, Now: fixedNow("2026-05-23")}, store)

	_, err := acc.Add(context.Background(), "sess-1", "openai", "gpt", 1000, 1000)
	if err == nil {
		t.Fatal("expected error from failing store, got nil")
	}
}

// TestAccumulator_StoreFailureRollsBackInMemory store.Append が失敗したとき
// in-memory aggregate が完全に直前状態へ巻き戻ることを確認する
// 旧実装は At/SessionID/Provider/Model を巻き戻していなかったため、初回 Add 失敗後に
// SessionTotal がゼロ値ではなく中途半端なスナップショットを返す問題があった
func TestAccumulator_StoreFailureRollsBackInMemory(t *testing.T) {
	t.Parallel()

	pricing := map[string]Pricing{"openai": {InputPerMillionJPY: 1, OutputPerMillionJPY: 1}}
	store := &failingStore{}
	acc := NewAccumulator(Config{Pricing: pricing, Now: fixedNow("2026-05-23")}, store)

	if _, err := acc.Add(context.Background(), "sess-1", "openai", "gpt", 1000, 1000); err == nil {
		t.Fatal("expected error from failing store")
	}
	got := acc.SessionTotal("sess-1")
	if got != (Snapshot{}) {
		t.Fatalf("初回 Add 失敗後の SessionTotal はゼロ値のはず, got %+v", got)
	}
	daily := acc.DailyTotal("2026-05-23")
	if daily != (Snapshot{}) {
		t.Fatalf("初回 Add 失敗後の DailyTotal はゼロ値のはず, got %+v", daily)
	}
}

// fixedNow テスト用に固定日付を返す
// 不正な date 文字列が渡されたら panic で早期失敗させる
func fixedNow(date string) func() time.Time {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic("fixedNow: invalid date " + date + ": " + err.Error())
	}
	return func() time.Time { return parsed }
}

// memoryStore in-memory 実装。ファイル永続化のテストは store_test.go で行う
type memoryStore struct {
	items []Snapshot
}

func newMemoryStore() *memoryStore { return &memoryStore{} }

func (m *memoryStore) Append(_ context.Context, s Snapshot) error {
	m.items = append(m.items, s)
	return nil
}

func (m *memoryStore) QuerySession(_ context.Context, id string) ([]Snapshot, error) {
	var out []Snapshot
	for _, s := range m.items {
		if s.SessionID == id {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *memoryStore) QueryDate(_ context.Context, date string) ([]Snapshot, error) {
	var out []Snapshot
	for _, s := range m.items {
		if s.At.Format("2006-01-02") == date {
			out = append(out, s)
		}
	}
	return out, nil
}

type failingStore struct{}

func (failingStore) Append(context.Context, Snapshot) error {
	return errors.New("store boom")
}
func (failingStore) QuerySession(context.Context, string) ([]Snapshot, error) {
	return nil, errors.New("store boom")
}
func (failingStore) QueryDate(context.Context, string) ([]Snapshot, error) {
	return nil, errors.New("store boom")
}
