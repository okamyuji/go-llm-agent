package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/obs"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// ParallelToolsOptions 並列ツール実行のランタイム設定
type ParallelToolsOptions struct {
	MaxConcurrency int
	FailFast       bool
}

// ParallelOutcome 並列ツール実行の結果。順序は入力 ToolCall の順を保つ
type ParallelOutcome struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// ExecuteToolsParallel 複数 ToolCall を errgroup と semaphore で並列実行する
// require_approval を含む call が 1 件でもあるとバリア方式に切り替え、すべて直列に実行する
func (s *service) ExecuteToolsParallel(ctx context.Context, sessionID string, calls []llm.ToolCall, opts ParallelToolsOptions) []ParallelOutcome {
	if len(calls) == 0 {
		return nil
	}
	requiresApproval := false
	if len(s.approvalRequired) > 0 {
		for _, c := range calls {
			if s.approvalRequired[c.Name] {
				requiresApproval = true
				break
			}
		}
	}
	if requiresApproval || opts.MaxConcurrency <= 1 {
		return s.executeSerial(ctx, sessionID, calls)
	}
	return s.executeConcurrent(ctx, sessionID, calls, opts)
}

// executeSerial calls を 1 件ずつ順番に実行する
func (s *service) executeSerial(ctx context.Context, sessionID string, calls []llm.ToolCall) []ParallelOutcome {
	out := make([]ParallelOutcome, len(calls))
	for i, c := range calls {
		out[i] = s.executeOne(ctx, sessionID, c)
	}
	return out
}

// executeConcurrent calls をセマフォ付き並列実行する。順序は idx で揃える
func (s *service) executeConcurrent(ctx context.Context, sessionID string, calls []llm.ToolCall, opts ParallelToolsOptions) []ParallelOutcome {
	out := make([]ParallelOutcome, len(calls))
	sem := make(chan struct{}, opts.MaxConcurrency)
	var wg sync.WaitGroup
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i, c := range calls {
		wg.Add(1)
		go func(idx int, call llm.ToolCall) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if cancelCtx.Err() != nil {
				out[idx] = ParallelOutcome{CallID: call.ID, Name: call.Name, Content: "cancelled", IsError: true}
				return
			}
			outcome := s.executeOne(cancelCtx, sessionID, call)
			out[idx] = outcome
			if opts.FailFast && outcome.IsError {
				cancel()
			}
		}(i, c)
	}
	wg.Wait()
	return out
}

// executeOne 単一 ToolCall を承認・実行・観測の全プロセスで処理する
func (s *service) executeOne(ctx context.Context, sessionID string, call llm.ToolCall) ParallelOutcome {
	if s.approver != nil && s.approvalRequired[call.Name] {
		apCtx := ctx
		var apCancel context.CancelFunc
		if s.approvalTimeout > 0 {
			apCtx, apCancel = context.WithTimeout(ctx, s.approvalTimeout)
		}
		d, aerr := s.approver.Request(apCtx, ApprovalRequest{RunID: sessionID, CallID: call.ID, ToolName: call.Name, Arguments: call.Arguments})
		if apCancel != nil {
			apCancel()
		}
		// loop.go の runReAct と同じ判定 (errors.Is(aerr, ErrApprovalTimeout)) を使う
		// timeout 以外のエラーは Approver 自体の致命的失敗として拒否扱い
		if aerr != nil && !errors.Is(aerr, ErrApprovalTimeout) {
			return ParallelOutcome{CallID: call.ID, Name: call.Name, Content: "approver failure: " + aerr.Error(), IsError: true}
		}
		if !d.Allowed {
			return ParallelOutcome{CallID: call.ID, Name: call.Name, Content: "tool execution denied: " + d.Reason, IsError: true}
		}
	}
	t, ok := s.tools.Lookup(call.Name)
	if !ok {
		return ParallelOutcome{CallID: call.ID, Name: call.Name, Content: "tool not found: " + call.Name, IsError: true}
	}
	execCtx := context.WithValue(ctx, tool.CorrelationKey(), call.ID)
	execCtx, span := obs.StartToolSpan(execCtx, call.Name, call.ID)
	start := time.Now()
	res, terr := t.Execute(execCtx, call.Arguments)
	ok2 := terr == nil && !res.IsError
	obs.RecordToolOutcome(execCtx, call.Name, ok2, time.Since(start))
	span.End()
	content := res.Content
	if terr != nil {
		content = terr.Error()
	}
	// runReAct と同じ untrusted ラッパを並列経路でも付与する (CodeRabbit #03)
	content = "[UNTRUSTED INPUT: tool=" + call.Name + "]\n" + content + "\n[END UNTRUSTED]"
	if s.redactor != nil {
		content = s.redactor.Redact(content)
	}
	return ParallelOutcome{CallID: call.ID, Name: call.Name, Content: content, IsError: terr != nil || res.IsError}
}
