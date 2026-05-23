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
	name  string
	delay time.Duration
	hits  *int32
}

func (st *sleepyTool) Spec() tool.Spec { return tool.Spec{Name: st.name} }
func (st *sleepyTool) Execute(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	atomic.AddInt32(st.hits, 1)
	select {
	case <-time.After(st.delay):
	case <-ctx.Done():
	}
	return tool.Result{Content: "ok-" + st.name}, nil
}

func TestExecuteToolsParallel_RunsConcurrently(t *testing.T) {
	t.Parallel()
	var hits int32
	reg := &fakeRegistry{tools: map[string]tool.Tool{
		"a": &sleepyTool{name: "a", delay: 50 * time.Millisecond, hits: &hits},
		"b": &sleepyTool{name: "b", delay: 50 * time.Millisecond, hits: &hits},
		"c": &sleepyTool{name: "c", delay: 50 * time.Millisecond, hits: &hits},
	}}
	s := &service{tools: reg}

	calls := []llm.ToolCall{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}, {ID: "3", Name: "c"}}
	start := time.Now()
	out := s.ExecuteToolsParallel(context.Background(), "sess", calls, ParallelToolsOptions{MaxConcurrency: 3})
	elapsed := time.Since(start)
	if len(out) != 3 {
		t.Fatalf("expected 3 outcomes, got %d", len(out))
	}
	// 直列なら 150ms 以上、並列なら 100ms 未満
	if elapsed > 120*time.Millisecond {
		t.Errorf("expected concurrent (<120ms), elapsed=%v", elapsed)
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
		approver:         AutoApprover{Allow: true},
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
	// 直列で 60ms 以上かかる
	if elapsed < 50*time.Millisecond {
		t.Errorf("expected serial fallback (>=50ms), elapsed=%v", elapsed)
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
