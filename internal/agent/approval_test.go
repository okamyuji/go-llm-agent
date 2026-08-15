package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAutoDecider_AllowAndDeny(t *testing.T) {
	t.Parallel()
	allowed, reason, err := AutoDecider{Allow: true}.Decide(context.Background(), ApprovalRequest{})
	if !allowed || reason != "" || err != nil {
		t.Fatalf("Allow=true は (true,\"\",nil) 期待 got (%v,%q,%v)", allowed, reason, err)
	}
	allowed, reason, err = AutoDecider{Allow: false, Reason: "x"}.Decide(context.Background(), ApprovalRequest{})
	if allowed || reason != "x" || err != nil {
		t.Fatalf("Allow=false は (false,\"x\",nil) 期待 got (%v,%q,%v)", allowed, reason, err)
	}
}

func TestBrokerDecider_Allow(t *testing.T) {
	t.Parallel()
	a := NewHTTPApprover()
	d := NewBrokerDecider(a)
	go func() {
		for !a.Submit(ApprovalDecision{RunID: "r", CallID: "c", Allowed: true}) {
			time.Sleep(time.Millisecond)
		}
	}()
	allowed, reason, err := d.Decide(context.Background(), ApprovalRequest{RunID: "r", CallID: "c"})
	if !allowed || reason != "" || err != nil {
		t.Fatalf("(true,\"\",nil) 期待 got (%v,%q,%v)", allowed, reason, err)
	}
}

func TestBrokerDecider_ExplicitDeny(t *testing.T) {
	t.Parallel()
	a := NewHTTPApprover()
	d := NewBrokerDecider(a)
	go func() {
		for !a.Submit(ApprovalDecision{RunID: "r", CallID: "c", Allowed: false, Reason: "x"}) {
			time.Sleep(time.Millisecond)
		}
	}()
	allowed, reason, err := d.Decide(context.Background(), ApprovalRequest{RunID: "r", CallID: "c"})
	if allowed || reason != "x" {
		t.Fatalf("(false,\"x\") 期待 got (%v,%q)", allowed, reason)
	}
	if err != nil {
		t.Fatalf("明示的な拒否は致命的失敗ではない got %v", err)
	}
}

func TestBrokerDecider_TimeoutDefaultsToDeny(t *testing.T) {
	t.Parallel()
	d := NewBrokerDecider(NewHTTPApprover())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	allowed, reason, err := d.Decide(ctx, ApprovalRequest{RunID: "r", CallID: "c"})
	if allowed {
		t.Fatal("タイムアウトは拒否期待")
	}
	if err != nil {
		t.Fatalf("タイムアウトは err にしない got %v", err)
	}
	if reason != approvalTimedOutReason {
		t.Fatalf("default_decision 解決の理由期待 got %q", reason)
	}
}

func TestBrokerDecider_PropagatesFatalError(t *testing.T) {
	t.Parallel()
	a := NewHTTPApprover()
	d := NewBrokerDecider(a)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = a.Request(ctx, ApprovalRequest{RunID: "r", CallID: "c"})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		_, pending := a.pending[approvalKey{RunID: "r", CallID: "c"}]
		a.mu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first Request did not register as pending")
		}
		time.Sleep(time.Millisecond)
	}
	// 同一キーの併走 Request は ErrApprovalAlreadyPending になる
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	allowed, reason, err := d.Decide(ctx, ApprovalRequest{RunID: "r", CallID: "c"})
	if err == nil || !errors.Is(err, ErrApprovalAlreadyPending) {
		t.Fatalf("致命的失敗の伝播期待 got %v", err)
	}
	if allowed || reason != "" {
		t.Fatalf("err 非 nil のとき (false,\"\") 期待 got (%v,%q)", allowed, reason)
	}
	a.Submit(ApprovalDecision{RunID: "r", CallID: "c", Allowed: false})
	<-done
}

func TestHTTPApprover_SubmitReleasesPendingRequest(t *testing.T) {
	t.Parallel()
	a := NewHTTPApprover()
	got := make(chan ApprovalDecision, 1)
	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d, err := a.Request(ctx, ApprovalRequest{RunID: "r1", CallID: "c1", ToolName: "shell"})
		errCh <- err
		got <- d
	}()
	// Request の内部 channel 登録が完了するまで Submit が失敗するため、短い間隔でリトライ
	deadline := time.Now().Add(2 * time.Second)
	submitted := false
	for time.Now().Before(deadline) {
		if a.Submit(ApprovalDecision{RunID: "r1", CallID: "c1", Allowed: true, Reviewer: "user"}) {
			submitted = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !submitted {
		t.Fatal("Submit must succeed against registered channel")
	}
	select {
	case d := <-got:
		if err := <-errCh; err != nil {
			t.Errorf("unexpected err from Request: %v", err)
		}
		if !d.Allowed {
			t.Errorf("expected Allowed=true, got %+v", d)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for decision")
	}
}

// TestHTTPApprover_TimeoutFailsClosed timeout 時は常に deny を返す fail-closed を確認する
// 旧仕様の defaultDeny=false (timeout で Allowed=true) は fail-open のため廃止した
// 旧 TestHTTPApprover_TimeoutDefaultDeny は同等のシナリオだったため統合した
func TestHTTPApprover_TimeoutFailsClosed(t *testing.T) {
	t.Parallel()
	a := NewHTTPApprover()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	d, err := a.Request(ctx, ApprovalRequest{RunID: "r3", CallID: "c3", ToolName: "shell"})
	if !errors.Is(err, ErrApprovalTimeout) {
		t.Fatalf("timeout は ErrApprovalTimeout, got %v", err)
	}
	if d.Allowed {
		t.Error("timeout 時は Allowed=false のはず")
	}
}

func TestHTTPApprover_SubmitUnknownIsFalse(t *testing.T) {
	t.Parallel()
	a := NewHTTPApprover()
	if a.Submit(ApprovalDecision{RunID: "x", CallID: "y", Allowed: true}) {
		t.Error("unknown RunID/CallID must return false")
	}
}
