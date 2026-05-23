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
// InputPerMillionJPY が入力トークン、OutputPerMillionJPY が出力トークンの
// 1,000,000 トークンあたり円換算単価
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

// ErrInvalidTokenCount 負のトークン数が渡された場合のセンチネルエラー
// 負数はランタイムで予算判定や累計値を破壊するため Add 段階で拒否する
var ErrInvalidTokenCount = errors.New("billing: token count must be non-negative")

// accumulator Accumulator の実装
// 集計はメモリ上に保持し、永続化は Store に委譲する
// 読み取り経路 (SessionTotal / DailyTotal) は RWMutex の RLock を使い、
// 書き込み経路 (Add) のみ Lock を取得することで読み取りの並行性能を確保する
type accumulator struct {
	cfg   Config
	store Store

	mu        sync.RWMutex
	sessions  map[string]Snapshot
	dailyCost map[string]float64
	daily     map[string]Snapshot
}

// NewAccumulator Accumulator を構築する
// Config.Now が nil のとき time.Now で代替する
// store が nil のときは Add 時の panic を防ぐため no-op の nopStore を採用する
// 呼び出し側で永続化が不要な場合 (テスト用途等) は明示的に NewNopStore() を渡すのが望ましい
func NewAccumulator(cfg Config, store Store) Accumulator {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if store == nil {
		store = nopStore{}
	}
	return &accumulator{
		cfg:       cfg,
		store:     store,
		sessions:  map[string]Snapshot{},
		dailyCost: map[string]float64{},
		daily:     map[string]Snapshot{},
	}
}

// nopStore Append/Query を何もせず返すフォールバック実装
// nil Store が渡されたときに nil panic を防ぐためだけに使う
type nopStore struct{}

func (nopStore) Append(context.Context, Snapshot) error                   { return nil }
func (nopStore) QuerySession(context.Context, string) ([]Snapshot, error) { return nil, nil }
func (nopStore) QueryDate(context.Context, string) ([]Snapshot, error)    { return nil, nil }

// Add 1 回の LLM 呼び出しのトークンを集計し、Store に永続化する
// 予算超過時は Snapshot を返さず ErrBudgetExceeded を返す。
// 予算判定・状態更新・store.Append を a.mu 内で一貫処理して同時呼び出しのレースを防ぐ。
// store.Append 失敗時は同じロック保持中に in-memory aggregate を巻き戻して divergence を防ぐ
func (a *accumulator) Add(ctx context.Context, sessionID, providerName, model string, in, out int) (Snapshot, error) {
	if in < 0 || out < 0 {
		return Snapshot{}, fmt.Errorf("%w: in=%d out=%d", ErrInvalidTokenCount, in, out)
	}
	now := a.cfg.Now().UTC()
	date := now.Format("2006-01-02")
	cost := computeCostJPY(a.cfg.Pricing[providerName], in, out)

	a.mu.Lock()
	defer a.mu.Unlock()

	// 失敗時に完全に巻き戻すための直前状態のスナップショットを保持する
	// At / SessionID / Provider / Model などフィールド単位の partial 戻しでは
	// 初回 Add 失敗時にゼロ値ではなく中途半端な状態が残ったままになる
	prevSess, sessExisted := a.sessions[sessionID]
	prevDaily, dailyExisted := a.daily[date]
	prevDailyCost, dailyCostExisted := a.dailyCost[date]

	sessSoFar := prevSess
	projectedTokens := sessSoFar.InputTokens + sessSoFar.OutputTokens + in + out
	projectedDailyCost := prevDailyCost + cost
	if a.cfg.Budget.SessionMaxTokens > 0 && projectedTokens > a.cfg.Budget.SessionMaxTokens {
		return Snapshot{}, fmt.Errorf("%w: session tokens %d > %d", ErrBudgetExceeded, projectedTokens, a.cfg.Budget.SessionMaxTokens)
	}
	if a.cfg.Budget.DailyMaxCostJPY > 0 && projectedDailyCost > a.cfg.Budget.DailyMaxCostJPY {
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

	dailySoFar := prevDaily
	dailySoFar.InputTokens += in
	dailySoFar.OutputTokens += out
	dailySoFar.CostJPY += cost
	dailySoFar.At = now
	a.daily[date] = dailySoFar
	a.dailyCost[date] = projectedDailyCost

	if err := a.store.Append(ctx, snap); err != nil {
		// 永続化失敗時は in-memory aggregate を直前状態に完全に巻き戻す
		// At / SessionID / Provider / Model も同時に戻すため、prev スナップショットを使う
		if sessExisted {
			a.sessions[sessionID] = prevSess
		} else {
			delete(a.sessions, sessionID)
		}
		if dailyExisted {
			a.daily[date] = prevDaily
		} else {
			delete(a.daily, date)
		}
		if dailyCostExisted {
			a.dailyCost[date] = prevDailyCost
		} else {
			delete(a.dailyCost, date)
		}
		return Snapshot{}, fmt.Errorf("billing append: %w", err)
	}
	return snap, nil
}

// SessionTotal セッション単位の累計を返す。未存在のセッションはゼロ値
// 読み取り経路なので RLock を使い、複数の集計クエリを並行して捌けるようにする
func (a *accumulator) SessionTotal(sessionID string) Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.sessions[sessionID]
}

// DailyTotal 日付単位の累計を返す。未存在の日付はゼロ値
// 読み取り経路なので RLock を使う
func (a *accumulator) DailyTotal(date string) Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.daily[date]
}

// computeCostJPY 単価とトークン数から円コストを算出する
func computeCostJPY(p Pricing, in, out int) float64 {
	const million = 1_000_000.0
	return float64(in)/million*p.InputPerMillionJPY + float64(out)/million*p.OutputPerMillionJPY
}
