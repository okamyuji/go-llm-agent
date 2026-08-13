package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// fakeRegistry tool.Registry の最小モック
type fakeRegistry struct {
	tools map[string]tool.Tool
}

func (r *fakeRegistry) Lookup(name string) (tool.Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}
func (r *fakeRegistry) List() []tool.Spec {
	out := make([]tool.Spec, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, tool.Spec{Name: name})
	}
	return out
}

// sleepyTool sleep してから OK を返すツール
type sleepyTool struct {
	name    string
	delay   time.Duration
	hits    *int32
	started chan struct{}   // nil の場合は未使用、設定されている場合は Execute 開始時に閉じる
	gate    <-chan struct{} // nil の場合は無視、設定されている場合は受信できるまで待機
}

func (st *sleepyTool) Spec() tool.Spec { return tool.Spec{Name: st.name} }
func (st *sleepyTool) Execute(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	atomic.AddInt32(st.hits, 1)
	if st.started != nil {
		close(st.started)
	}
	if st.gate != nil {
		select {
		case <-st.gate:
		case <-ctx.Done():
		}
	} else {
		select {
		case <-time.After(st.delay):
		case <-ctx.Done():
		}
	}
	return tool.Result{Content: "ok-" + st.name}, nil
}

func TestExecuteToolsParallel_RunsConcurrently(t *testing.T) {
	t.Parallel()
	var hits int32
	gate := make(chan struct{})
	startA := make(chan struct{})
	startB := make(chan struct{})
	startC := make(chan struct{})
	reg := &fakeRegistry{tools: map[string]tool.Tool{
		"a": &sleepyTool{name: "a", hits: &hits, started: startA, gate: gate},
		"b": &sleepyTool{name: "b", hits: &hits, started: startB, gate: gate},
		"c": &sleepyTool{name: "c", hits: &hits, started: startC, gate: gate},
	}}
	s := &service{tools: reg}

	calls := []llm.ToolCall{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}, {ID: "3", Name: "c"}}

	resultCh := make(chan []ParallelOutcome, 1)
	go func() {
		resultCh <- s.ExecuteToolsParallel(context.Background(), "sess", calls, ParallelToolsOptions{MaxConcurrency: 3})
	}()

	// 3 件すべてが Execute に入ったことを確認 (同時起動の決定論的検出)
	waitStarted := func(ch chan struct{}, name string) {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatalf("tool %s did not start within timeout", name)
		}
	}
	waitStarted(startA, "a")
	waitStarted(startB, "b")
	waitStarted(startC, "c")
	close(gate)

	out := <-resultCh
	if len(out) != 3 {
		t.Fatalf("expected 3 outcomes, got %d", len(out))
	}
}

func TestExecuteToolsParallel_PreservesOrder(t *testing.T) {
	t.Parallel()
	var hits int32
	reg := &fakeRegistry{tools: map[string]tool.Tool{
		"a": &sleepyTool{name: "a", delay: time.Millisecond, hits: &hits},
		"b": &sleepyTool{name: "b", delay: time.Millisecond, hits: &hits},
	}}
	s := &service{tools: reg}
	calls := []llm.ToolCall{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}}
	out := s.ExecuteToolsParallel(context.Background(), "sess", calls, ParallelToolsOptions{MaxConcurrency: 2})
	if out[0].CallID != "1" || out[1].CallID != "2" {
		t.Errorf("order broken: %+v", out)
	}
}

func TestExecuteToolsParallel_FallsBackToSerialWhenApprovalRequired(t *testing.T) {
	t.Parallel()
	var hits int32
	reg := &fakeRegistry{tools: map[string]tool.Tool{
		"safe":      &sleepyTool{name: "safe", delay: 30 * time.Millisecond, hits: &hits},
		"dangerous": &sleepyTool{name: "dangerous", delay: 30 * time.Millisecond, hits: &hits},
	}}
	s := &service{
		tools:            reg,
		decider:          AutoDecider{Allow: true},
		approvalRequired: map[string]bool{"dangerous": true},
		approvalTimeout:  time.Second,
	}
	calls := []llm.ToolCall{{ID: "1", Name: "safe"}, {ID: "2", Name: "dangerous"}}
	start := time.Now()
	out := s.ExecuteToolsParallel(context.Background(), "sess", calls, ParallelToolsOptions{MaxConcurrency: 10})
	elapsed := time.Since(start)
	if len(out) != 2 {
		t.Fatalf("expected 2 outcomes, got %d", len(out))
	}
	// 直列で 2 ツール × 30ms = 60ms 以上かかる。並列だったら 30ms 程度で済むので
	// 55ms をボーダーとして直列フォールバックを検出する
	if elapsed < 55*time.Millisecond {
		t.Errorf("expected serial fallback (>=55ms), elapsed=%v", elapsed)
	}
}

func TestExecuteToolsParallel_EmptyReturnsNil(t *testing.T) {
	t.Parallel()
	s := &service{}
	if got := s.ExecuteToolsParallel(context.Background(), "sess", nil, ParallelToolsOptions{}); got != nil {
		t.Errorf("expected nil for empty calls, got %v", got)
	}
}

func TestExecuteToolsParallel_MissingToolIsError(t *testing.T) {
	t.Parallel()
	reg := &fakeRegistry{tools: map[string]tool.Tool{}}
	s := &service{tools: reg}
	calls := []llm.ToolCall{{ID: "1", Name: "missing"}}
	out := s.ExecuteToolsParallel(context.Background(), "sess", calls, ParallelToolsOptions{MaxConcurrency: 2})
	if !out[0].IsError {
		t.Errorf("missing tool must yield IsError=true")
	}
}
