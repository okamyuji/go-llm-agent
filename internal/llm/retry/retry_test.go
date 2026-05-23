package retry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func TestBackoff_GrowsExponentiallyAndCapsAtMax(t *testing.T) {
	t.Parallel()

	c := Config{
		MaxAttempts:    5,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     500 * time.Millisecond,
		JitterRatio:    0,
	}
	want := []time.Duration{100, 200, 400, 500, 500}
	for i, exp := range want {
		got := backoffForAttempt(c, i)
		if got != exp*time.Millisecond {
			t.Errorf("attempt %d backoff = %v want %v", i, got, exp*time.Millisecond)
		}
	}
}

func TestBackoff_JitterStaysWithinRatio(t *testing.T) {
	t.Parallel()

	c := Config{
		MaxAttempts:    3,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     1 * time.Second,
		JitterRatio:    0.5,
	}
	for i := range 100 {
		got := backoffForAttempt(c, 0)
		min := 50 * time.Millisecond
		max := 150 * time.Millisecond
		if got < min || got > max {
			t.Fatalf("iter %d backoff %v out of [%v, %v]", i, got, min, max)
		}
	}
}

// retryProvider 設定で 429 を N 回返した後に成功する fake provider
type retryProvider struct {
	failures int32
	attempts int32
}

func (p *retryProvider) Name() string { return "retryfake" }

func (p *retryProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	atomic.AddInt32(&p.attempts, 1)
	if atomic.LoadInt32(&p.failures) > 0 {
		atomic.AddInt32(&p.failures, -1)
		return nil, &llm.ProviderError{Provider: "retryfake", StatusCode: 429, Retryable: true, Underlying: errors.New("rate limited")}
	}
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}, nil
}

func (p *retryProvider) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	if _, err := p.Chat(ctx, req); err != nil {
		return nil, err
	}
	return &emptyStream{}, nil
}

type emptyStream struct{ done bool }

func (e *emptyStream) Recv() (llm.StreamEvent, bool) {
	if e.done {
		return llm.StreamEvent{}, false
	}
	e.done = true
	return llm.StreamEvent{DeltaText: "ok"}, true
}
func (e *emptyStream) Close() error { return nil }

func TestWrapProvider_RetriesUntilSuccess(t *testing.T) {
	t.Parallel()

	prov := &retryProvider{failures: 2}
	wrapped := WrapProvider("retryfake", prov, Config{
		MaxAttempts:    4,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
		JitterRatio:    0,
	})
	resp, err := wrapped.Chat(context.Background(), llm.ChatRequest{Model: "x"})
	if err != nil {
		t.Fatalf("Chat returned error after retries: %v", err)
	}
	if resp.Message.Content != "ok" {
		t.Errorf("unexpected content: %q", resp.Message.Content)
	}
	if atomic.LoadInt32(&prov.attempts) != 3 {
		t.Errorf("attempts = %d want 3", prov.attempts)
	}
}

func TestWrapProvider_StopsAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	prov := &retryProvider{failures: 10}
	wrapped := WrapProvider("retryfake", prov, Config{
		MaxAttempts:    3,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
		JitterRatio:    0,
	})
	_, err := wrapped.Chat(context.Background(), llm.ChatRequest{Model: "x"})
	if err == nil {
		t.Fatal("expected error after MaxAttempts exhausted")
	}
	if !errors.Is(err, ErrAllAttemptsFailed) {
		t.Errorf("expected ErrAllAttemptsFailed, got %v", err)
	}
}

// nonRetryableProvider Retryable=false の ProviderError を返す
type nonRetryableProvider struct{ attempts int32 }

func (p *nonRetryableProvider) Name() string { return "nonretry" }
func (p *nonRetryableProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	atomic.AddInt32(&p.attempts, 1)
	return nil, &llm.ProviderError{Provider: "nonretry", StatusCode: 400, Retryable: false, Underlying: errors.New("bad request")}
}
func (p *nonRetryableProvider) Stream(_ context.Context, _ llm.ChatRequest) (llm.ChatStream, error) {
	return nil, &llm.ProviderError{Provider: "nonretry", StatusCode: 400, Retryable: false, Underlying: errors.New("bad request")}
}

func TestWrapProvider_NonRetryableErrorFailsFast(t *testing.T) {
	t.Parallel()

	prov := &nonRetryableProvider{}
	wrapped := WrapProvider("nonretry", prov, Config{
		MaxAttempts:    5,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})
	_, err := wrapped.Chat(context.Background(), llm.ChatRequest{Model: "x"})
	if err == nil {
		t.Fatal("expected error from non-retryable provider")
	}
	if atomic.LoadInt32(&prov.attempts) != 1 {
		t.Errorf("attempts = %d want 1 (non-retryable should not retry)", prov.attempts)
	}
}

func TestWrapProvider_StreamRetriesOnInitialError(t *testing.T) {
	t.Parallel()

	prov := &retryProvider{failures: 1}
	wrapped := WrapProvider("retryfake", prov, Config{
		MaxAttempts:    3,
		InitialBackoff: 1 * time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	})
	stream, err := wrapped.Stream(context.Background(), llm.ChatRequest{Model: "x"})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	defer func() { _ = stream.Close() }()
	if atomic.LoadInt32(&prov.attempts) != 2 {
		t.Errorf("Stream attempts = %d want 2", prov.attempts)
	}
}

func TestWrapProvider_DisabledWhenMaxAttemptsLE1(t *testing.T) {
	t.Parallel()

	prov := &retryProvider{failures: 0}
	got := WrapProvider("retryfake", prov, Config{MaxAttempts: 1})
	if got != prov {
		t.Fatal("WrapProvider must return inner provider when MaxAttempts <= 1")
	}
}

func TestWrapProvider_DefaultsAppliedWhenZeros(t *testing.T) {
	t.Parallel()

	prov := &retryProvider{failures: 1}
	wrapped := WrapProvider("retryfake", prov, Config{MaxAttempts: 2}).(*wrapped)
	if wrapped.cfg.InitialBackoff <= 0 {
		t.Error("InitialBackoff must default when 0 was provided")
	}
	if wrapped.cfg.MaxBackoff <= 0 {
		t.Error("MaxBackoff must default when 0 was provided")
	}
}

func TestIsRetryable_CanceledIsNotRetryable(t *testing.T) {
	t.Parallel()
	if isRetryable(context.Canceled) {
		t.Error("context.Canceled must not be retryable")
	}
	if isRetryable(nil) {
		t.Error("nil error must not be retryable")
	}
	if isRetryable(errors.New("plain")) {
		t.Error("plain error must not be retryable")
	}
}

func TestWrapProvider_ContextCancellationStopsRetry(t *testing.T) {
	t.Parallel()

	prov := &retryProvider{failures: 100}
	wrapped := WrapProvider("retryfake", prov, Config{
		MaxAttempts:    10,
		InitialBackoff: 50 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := wrapped.Chat(ctx, llm.ChatRequest{Model: "x"})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	// timeoutが起きた直後の attempt は1〜2 程度。10回試行する前に止まることが重要
	if atomic.LoadInt32(&prov.attempts) >= 10 {
		t.Errorf("attempts = %d should have stopped early on ctx cancel", prov.attempts)
	}
}
