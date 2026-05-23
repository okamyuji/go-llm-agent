// Package main retry Decorator の動作確認用フィクスチャ
// 429 を 2 回返した後成功する fake provider をリトライ層で包み、
// E2E スクリプト 03-llm-retry.sh から実行される
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/retry"
)

type prov struct {
	failures atomic.Int32
	calls    atomic.Int32
}

func (p *prov) Name() string { return "fakeretry" }

func (p *prov) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls.Add(1)
	if p.failures.Load() > 0 {
		p.failures.Add(-1)
		return nil, &llm.ProviderError{Provider: "fakeretry", StatusCode: 429, Retryable: true, Underlying: errors.New("rate limited")}
	}
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}}, nil
}

func (p *prov) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	if _, err := p.Chat(ctx, req); err != nil {
		return nil, err
	}
	return emptyStream{}, nil
}

type emptyStream struct{}

func (emptyStream) Recv() (llm.StreamEvent, bool) { return llm.StreamEvent{}, false }
func (emptyStream) Close() error                  { return nil }

func main() {
	p := &prov{}
	p.failures.Store(2)
	wrapped := retry.WrapProvider("fakeretry", p, retry.Config{
		MaxAttempts:    5,
		InitialBackoff: 5 * time.Millisecond,
		MaxBackoff:     20 * time.Millisecond,
		JitterRatio:    0,
	})
	resp, err := wrapped.Chat(context.Background(), llm.ChatRequest{Model: "x"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR:", err)
		os.Exit(1)
	}
	fmt.Printf("calls=%d content=%q\n", p.calls.Load(), resp.Message.Content)
}
