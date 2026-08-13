package cliui

import (
	"context"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
)

func TestApprovalPrompter_DecideSendsRequest(t *testing.T) {
	p := NewApprovalPrompter()
	go func() {
		_, _, _ = p.Decide(context.Background(), agent.ApprovalRequest{ToolName: "fs_write", Summary: "+a"})
	}()
	select {
	case pr := <-p.requestsCh():
		if pr.req.ToolName != "fs_write" || pr.req.Summary != "+a" {
			t.Fatalf("要求の内容が渡る期待 got %+v", pr.req)
		}
		pr.reply <- approvalAnswer{allowed: true}
	case <-time.After(time.Second):
		t.Fatal("要求が届かない")
	}
}

func TestApprovalPrompter_DecideWaitsForReply(t *testing.T) {
	tests := []struct {
		name       string
		answer     approvalAnswer
		wantAllow  bool
		wantReason string
	}{
		{"approve", approvalAnswer{allowed: true}, true, ""},
		{"deny", approvalAnswer{allowed: false, reason: "x"}, false, "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewApprovalPrompter()
			type result struct {
				allowed bool
				reason  string
				err     error
			}
			res := make(chan result, 1)
			go func() {
				a, r, e := p.Decide(context.Background(), agent.ApprovalRequest{})
				res <- result{a, r, e}
			}()
			pr := <-p.requestsCh()
			pr.reply <- tc.answer
			got := <-res
			if got.allowed != tc.wantAllow || got.reason != tc.wantReason || got.err != nil {
				t.Fatalf("(%v,%q,nil) 期待 got (%v,%q,%v)", tc.wantAllow, tc.wantReason, got.allowed, got.reason, got.err)
			}
		})
	}
}

func TestApprovalPrompter_CtxDoneBeforeSend(t *testing.T) {
	p := NewApprovalPrompter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	allowed, reason, err := p.Decide(ctx, agent.ApprovalRequest{})
	if allowed || err != nil || reason != approvalDeniedByTimeout {
		t.Fatalf("送信前 ctx Done は deny 解決期待 got (%v,%q,%v)", allowed, reason, err)
	}
	select {
	case <-p.requestsCh():
		t.Fatal("要求は送信されない期待")
	default:
	}
}

func TestApprovalPrompter_CtxDoneWaitingReply(t *testing.T) {
	p := NewApprovalPrompter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		allowed bool
		reason  string
		err     error
	}
	res := make(chan result, 1)
	go func() {
		a, r, e := p.Decide(ctx, agent.ApprovalRequest{})
		res <- result{a, r, e}
	}()
	<-p.requestsCh() // 受信するが応答しない
	cancel()
	got := <-res
	if got.allowed || got.err != nil || got.reason != approvalDeniedByTimeout {
		t.Fatalf("応答前 ctx Done は deny 解決期待 got %+v", got)
	}
}

func TestApprovalPrompter_AbandonedRequestDoesNotLeakToNextTurn(t *testing.T) {
	p := NewApprovalPrompter()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = p.Decide(ctx, agent.ApprovalRequest{ToolName: "abandoned"})
	}()
	// 誰も受信しないまま ctx を Done にする
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	select {
	case pr := <-p.requestsCh():
		t.Fatalf("バッファ 0 なので残留要求は存在しない期待 got %+v", pr.req)
	case <-time.After(20 * time.Millisecond):
	}

	// 続けて別の Decide を呼ぶと、その新しい要求だけが届く
	go func() {
		_, _, _ = p.Decide(context.Background(), agent.ApprovalRequest{ToolName: "fresh"})
	}()
	select {
	case pr := <-p.requestsCh():
		if pr.req.ToolName != "fresh" {
			t.Fatalf("新しい要求だけが届く期待 got %q", pr.req.ToolName)
		}
		pr.reply <- approvalAnswer{}
	case <-time.After(time.Second):
		t.Fatal("新しい要求が届かない")
	}
}
