package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// recordingDecider 受け取った ApprovalRequest を記録し固定の判断を返す
type recordingDecider struct {
	allowed  bool
	reason   string
	err      error
	block    time.Duration
	requests []agent.ApprovalRequest
}

func (d *recordingDecider) Decide(ctx context.Context, req agent.ApprovalRequest) (bool, string, error) {
	d.requests = append(d.requests, req)
	if d.block > 0 {
		select {
		case <-ctx.Done():
			// タイムアウトは default_decision へ解決する (拒否)。err にしない
			return false, "approval timed out; default_decision=deny", nil
		case <-time.After(d.block):
		}
	}
	return d.allowed, d.reason, d.err
}

func approvalService(t *testing.T, d agent.ApprovalDecider, toolName string, calls *int, timeout time.Duration) (agent.Service, *fakeProvider) {
	t.Helper()
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: toolName, Arguments: json.RawMessage(`{"path":"/tmp/x.txt","content":"new\n"}`)}}},
		{{DeltaText: "done"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{namedTool{name: toolName, content: "executed", calls: calls}}, []string{toolName})
	svc := agent.New(fakeReg{p: prov}, tools, agent.WithApprovalDecider(d, []string{toolName}, timeout))
	return svc, prov
}

func TestRunReAct_ApprovalDenied_ToolResultIsError(t *testing.T) {
	calls := 0
	svc, _ := approvalService(t, &recordingDecider{allowed: false, reason: "denied by user"}, "fs_write", &calls, time.Second)

	events, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "書き込んで"}},
		MaxToolHops: 3,
	})
	if err != nil {
		t.Fatalf("拒否はターンを打ち切らない期待 got %v", err)
	}
	if calls != 0 {
		t.Fatalf("拒否時はツールを実行しない期待 got %d", calls)
	}
	var found bool
	for _, ev := range events {
		if ev.Kind == agent.EventToolResult {
			found = true
			if !ev.ToolResult.IsError {
				t.Fatal("拒否のツール結果は IsError 期待")
			}
			if !strings.Contains(ev.ToolResult.Content, "tool execution denied by reviewer: denied by user") {
				t.Fatalf("拒否理由を含む期待 got %q", ev.ToolResult.Content)
			}
		}
	}
	if !found {
		t.Fatal("EventToolResult 期待")
	}
}

func TestRunReAct_ApprovalDeniedWithoutReason_UsesDefaultText(t *testing.T) {
	calls := 0
	svc, _ := approvalService(t, &recordingDecider{allowed: false}, "fs_write", &calls, time.Second)

	events, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "書き込んで"}},
		MaxToolHops: 3,
	})
	if err != nil {
		t.Fatalf("拒否はターンを打ち切らない期待 got %v", err)
	}
	for _, ev := range events {
		if ev.Kind == agent.EventToolResult && ev.ToolResult.Content != "tool execution denied by reviewer" {
			t.Fatalf("理由が空なら既定文言のみ期待 got %q", ev.ToolResult.Content)
		}
	}
}

func TestRunReAct_ApprovalTimeout_TreatedAsDeny(t *testing.T) {
	calls := 0
	d := &recordingDecider{allowed: true, block: 5 * time.Second}
	svc, _ := approvalService(t, d, "fs_write", &calls, 30*time.Millisecond)

	events, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "書き込んで"}},
		MaxToolHops: 3,
	})
	if err != nil {
		t.Fatalf("タイムアウトはターンを打ち切らない期待 got %v", err)
	}
	if calls != 0 {
		t.Fatalf("タイムアウト時はツールを実行しない期待 got %d", calls)
	}
	for _, ev := range events {
		if ev.Kind == agent.EventError {
			t.Fatalf("タイムアウトで EventError を出さない期待 got %v", ev.Err)
		}
	}
}

func TestRunReAct_ApprovalFatalError_EmitsEventError(t *testing.T) {
	calls := 0
	fatal := errors.New("approval broker unavailable")
	svc, _ := approvalService(t, &recordingDecider{err: fatal}, "fs_write", &calls, time.Second)

	events, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "書き込んで"}},
		MaxToolHops: 3,
	})
	if !errors.Is(err, fatal) {
		t.Fatalf("致命的失敗はターンを打ち切る期待 got %v", err)
	}
	if calls != 0 {
		t.Fatalf("ツールを実行しない期待 got %d", calls)
	}
	var sawError bool
	for _, ev := range events {
		if ev.Kind == agent.EventError {
			sawError = true
		}
		if ev.Kind == agent.EventToolResult {
			t.Fatal("致命的失敗は拒否のツール結果として吸わない期待")
		}
	}
	if !sawError {
		t.Fatal("EventError 期待")
	}
}

func TestRunReAct_ApprovalAllowed_ExecutesTool(t *testing.T) {
	calls := 0
	svc, _ := approvalService(t, &recordingDecider{allowed: true}, "fs_write", &calls, time.Second)

	_, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "書き込んで"}},
		MaxToolHops: 3,
	})
	if err != nil {
		t.Fatalf("承認時は通常どおり完了する期待 got %v", err)
	}
	if calls != 1 {
		t.Fatalf("ツールが 1 回実行される期待 got %d", calls)
	}
}

func TestRunReAct_ApprovalSummaryPassedToDecider(t *testing.T) {
	calls := 0
	d := &recordingDecider{allowed: true}
	svc, _ := approvalService(t, d, "fs_write", &calls, time.Second)

	if _, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		SessionID:   "",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "書き込んで"}},
		MaxToolHops: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if len(d.requests) != 1 {
		t.Fatalf("承認要求 1 件期待 got %d", len(d.requests))
	}
	req := d.requests[0]
	if !strings.Contains(req.Summary, "+new") {
		t.Fatalf("diff マーカーを含むサマリ期待 got %q", req.Summary)
	}
	if req.RunID != "default" {
		t.Fatalf("空 SessionID は default へ正規化される期待 got %q", req.RunID)
	}
	if req.ToolName != "fs_write" || req.CallID != "c1" {
		t.Fatalf("ツール名と CallID が渡る期待 got %+v", req)
	}
}

func TestRunReAct_ApprovalNotRequiredForOtherTools(t *testing.T) {
	calls := 0
	d := &recordingDecider{allowed: false, reason: "deny all"}
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{{DeltaText: "done"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{namedTool{name: "probe", content: "ok", calls: &calls}}, []string{"probe"})
	svc := agent.New(fakeReg{p: prov}, tools, agent.WithApprovalDecider(d, []string{"fs_write"}, time.Second))

	if _, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("required_tools 外は承認不要で実行される期待 got %d", calls)
	}
	if len(d.requests) != 0 {
		t.Fatalf("承認を要求しない期待 got %d", len(d.requests))
	}
}
