package agent

import (
	"context"
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
			// セマフォ取得後に cancel 状態を確認することで、待機中に他ゴルーチンが
			// FailFast cancel した場合でも executeOne を起動せず即時離脱する
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
	if s.decider != nil && s.approvalRequired[call.Name] {
		// loop.go の runReAct と同じ正規化を適用する (runID の lookup を一致させる)
		// 空 sessionID で発行された承認待ちが parallel 経路と sequential 経路で異なるキーに
		// 振り分けられると、Submit が空振りして deadlock 経路に入る
		runID := sessionID
		if runID == "" {
			runID = "default"
		}
		timeout := s.approvalTimeout
		if timeout <= 0 {
			timeout = defaultApprovalTimeout
		}
		summary := BuildApprovalSummary(ctx, s.tools, call.Name, call.Arguments)
		apCtx, apCancel := context.WithTimeout(ctx, timeout)
		allowed, reason, derr := s.decider.Decide(apCtx, ApprovalRequest{
			RunID: runID, CallID: call.ID, ToolName: call.Name, Arguments: call.Arguments, Summary: summary,
		})
		apCancel()
		// loop.go の runReAct と同じ規約。derr は承認機構自体の致命的失敗のみで、
		// タイムアウトは decider 側が default_decision へ解決済み
		if derr != nil {
			return ParallelOutcome{CallID: call.ID, Name: call.Name, Content: "approver failure: " + derr.Error(), IsError: true}
		}
		if !allowed {
			return ParallelOutcome{CallID: call.ID, Name: call.Name, Content: "tool execution denied: " + reason, IsError: true}
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
	// runReAct と同じく、untrusted ラッパは無条件付与する
	// ツール側が自前マーカーを偽装してラップ回避するのを防ぐため接頭辞 check は外す
	content = wrapUntrusted(content, call.Name)
	if s.redactor != nil {
		content = s.redactor.Redact(content)
	}
	return ParallelOutcome{CallID: call.ID, Name: call.Name, Content: content, IsError: terr != nil || res.IsError}
}
