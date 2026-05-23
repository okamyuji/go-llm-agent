// Package retry LLM プロバイダー呼び出しのリトライとバックオフを提供する
// 5xx と 429 と context タイムアウトのうち ProviderError.Retryable=true のものだけが
// リトライ対象になり、それ以外は即座にエラーを返す
package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/obs"
)

// Config WrapProvider に渡すリトライ設定
type Config struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	JitterRatio    float64
}

// ErrAllAttemptsFailed すべての試行が失敗した場合に返す sentinel error
var ErrAllAttemptsFailed = errors.New("retry: all attempts failed")

// wrapped Provider Decorator
type wrapped struct {
	name  string
	inner llm.Provider
	cfg   Config
}

// WrapProvider llm.Provider をリトライ機能で包む
// cfg.MaxAttempts <= 1 の場合は何もせず inner をそのまま返す
func WrapProvider(name string, p llm.Provider, cfg Config) llm.Provider {
	if cfg.MaxAttempts <= 1 {
		return p
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 200 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 5 * time.Second
	}
	return &wrapped{name: name, inner: p, cfg: cfg}
}

// Name 包み込んだ Provider 名を返す
func (w *wrapped) Name() string { return w.inner.Name() }

// Chat リトライ付きで Chat を実行する
func (w *wrapped) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	var lastErr error
	for attempt := range w.cfg.MaxAttempts {
		resp, err := w.inner.Chat(ctx, req)
		if err == nil {
			return resp, nil
		}
		if !isRetryable(err) {
			return nil, err
		}
		lastErr = err
		if attempt == w.cfg.MaxAttempts-1 {
			break
		}
		obs.RecordRetry(ctx, w.name, attempt+1)
		if err := sleep(ctx, backoffForAttempt(w.cfg, attempt)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrAllAttemptsFailed, lastErr)
}

// Stream リトライ付きで Stream を実行する。Stream() 呼び出し自体のエラーのみリトライ対象
// ストリーム途中のエラーはコンテンツの一貫性が保てないため対象外とする
func (w *wrapped) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	var lastErr error
	for attempt := range w.cfg.MaxAttempts {
		stream, err := w.inner.Stream(ctx, req)
		if err == nil {
			return stream, nil
		}
		if !isRetryable(err) {
			return nil, err
		}
		lastErr = err
		if attempt == w.cfg.MaxAttempts-1 {
			break
		}
		obs.RecordRetry(ctx, w.name, attempt+1)
		if err := sleep(ctx, backoffForAttempt(w.cfg, attempt)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrAllAttemptsFailed, lastErr)
}

// isRetryable ProviderError.Retryable=true もしくは context.DeadlineExceeded で判定する
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var pe *llm.ProviderError
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	return false
}

// backoffForAttempt attempt 番目の試行に対するスリープ時間を計算する
// 指数 (InitialBackoff * 2^attempt) に MaxBackoff の上限を適用し
// JitterRatio に従って 1 ± JitterRatio の範囲で乱数を掛ける
func backoffForAttempt(c Config, attempt int) time.Duration {
	if c.InitialBackoff <= 0 {
		return 0
	}
	d := c.InitialBackoff << attempt
	if d <= 0 || d > c.MaxBackoff {
		d = c.MaxBackoff
	}
	if c.JitterRatio > 0 {
		// jitter のための疑似乱数は暗号用途ではなく、バックオフ時刻のばらけ生成にのみ使う
		factor := 1.0 + (rand.Float64()*2-1)*c.JitterRatio //nolint:gosec // backoff jitter, not cryptographic
		d = time.Duration(float64(d) * factor)
		if d <= 0 {
			d = 1
		}
	}
	return d
}

// sleep ctx の cancel と d の経過のいずれか早い方で復帰する
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
