// Package billing トークン使用量とコストの集計を提供する
// agent loop からの呼び出しでセッション単位/日次の合計を蓄積し、
// 設定された予算上限の超過を ErrBudgetExceeded で通知する
package billing

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Pricing 1 プロバイダーあたりの単価設定
// InputPerMillionJPY と OutputPerMillionJPY は 1,000,000 トークンあたりの円換算単価
type Pricing struct {
	InputPerMillionJPY  float64
	OutputPerMillionJPY float64
}

// Budget 予算上限
// 0 のフィールドは無制限として扱う
type Budget struct {
	SessionMaxTokens int
	DailyMaxCostJPY  float64
}

// Snapshot 1 回の Add の結果と永続化レコードを兼ねる
type Snapshot struct {
	SessionID    string    `json:"session_id"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostJPY      float64   `json:"cost_jpy"`
	At           time.Time `json:"at"`
}

// Config Accumulator の依存設定
type Config struct {
	Pricing map[string]Pricing
	Budget  Budget
	Now     func() time.Time
}

// Store 永続化バックエンドの抽象
type Store interface {
	Append(ctx context.Context, s Snapshot) error
	QuerySession(ctx context.Context, sessionID string) ([]Snapshot, error)
	QueryDate(ctx context.Context, date string) ([]Snapshot, error)
}

// Accumulator トークン集計の公開インターフェース
type Accumulator interface {
	Add(ctx context.Context, sessionID, providerName, model string, in, out int) (Snapshot, error)
	SessionTotal(sessionID string) Snapshot
	DailyTotal(date string) Snapshot
}

// ErrBudgetExceeded 予算超過を示すセンチネルエラー
var ErrBudgetExceeded = errors.New("billing: budget exceeded")

// accumulator Accumulator の実装
// 集計はメモリ上に保持し、永続化は Store に委譲する
type accumulator struct {
	cfg   Config
	store Store

	mu        sync.Mutex
	sessions  map[string]Snapshot
	dailyCost map[string]float64
	daily     map[string]Snapshot
}

// NewAccumulator Accumulator を構築する
// Config.Now が nil の場合は time.Now を使う
func NewAccumulator(cfg Config, store Store) Accumulator {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &accumulator{
		cfg:       cfg,
		store:     store,
		sessions:  map[string]Snapshot{},
		dailyCost: map[string]float64{},
		daily:     map[string]Snapshot{},
	}
}

// Add 1 回の LLM 呼び出しのトークンを集計し、Store に永続化する
// 予算超過時は Snapshot を返さず ErrBudgetExceeded を返す
func (a *accumulator) Add(ctx context.Context, sessionID, providerName, model string, in, out int) (Snapshot, error) {
	now := a.cfg.Now().UTC()
	date := now.Format("2006-01-02")
	cost := computeCostJPY(a.cfg.Pricing[providerName], in, out)

	a.mu.Lock()
	sessSoFar := a.sessions[sessionID]
	projectedTokens := sessSoFar.InputTokens + sessSoFar.OutputTokens + in + out
	projectedDailyCost := a.dailyCost[date] + cost
	if a.cfg.Budget.SessionMaxTokens > 0 && projectedTokens > a.cfg.Budget.SessionMaxTokens {
		a.mu.Unlock()
		return Snapshot{}, fmt.Errorf("%w: session tokens %d > %d", ErrBudgetExceeded, projectedTokens, a.cfg.Budget.SessionMaxTokens)
	}
	if a.cfg.Budget.DailyMaxCostJPY > 0 && projectedDailyCost > a.cfg.Budget.DailyMaxCostJPY {
		a.mu.Unlock()
		return Snapshot{}, fmt.Errorf("%w: daily cost %.2f > %.2f", ErrBudgetExceeded, projectedDailyCost, a.cfg.Budget.DailyMaxCostJPY)
	}

	snap := Snapshot{
		SessionID:    sessionID,
		Provider:     providerName,
		Model:        model,
		InputTokens:  in,
		OutputTokens: out,
		CostJPY:      cost,
		At:           now,
	}

	sessSoFar.SessionID = sessionID
	sessSoFar.Provider = providerName
	sessSoFar.Model = model
	sessSoFar.InputTokens += in
	sessSoFar.OutputTokens += out
	sessSoFar.CostJPY += cost
	sessSoFar.At = now
	a.sessions[sessionID] = sessSoFar

	dailySoFar := a.daily[date]
	dailySoFar.InputTokens += in
	dailySoFar.OutputTokens += out
	dailySoFar.CostJPY += cost
	dailySoFar.At = now
	a.daily[date] = dailySoFar
	a.dailyCost[date] = projectedDailyCost
	a.mu.Unlock()

	if err := a.store.Append(ctx, snap); err != nil {
		return Snapshot{}, fmt.Errorf("billing append: %w", err)
	}
	return snap, nil
}

// SessionTotal セッション単位の累計を返す。未存在のセッションはゼロ値
func (a *accumulator) SessionTotal(sessionID string) Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[sessionID]
}

// DailyTotal 日付単位の累計を返す。未存在の日付はゼロ値
func (a *accumulator) DailyTotal(date string) Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.daily[date]
}

// computeCostJPY 単価とトークン数から円コストを算出する
func computeCostJPY(p Pricing, in, out int) float64 {
	const million = 1_000_000.0
	return float64(in)/million*p.InputPerMillionJPY + float64(out)/million*p.OutputPerMillionJPY
}
