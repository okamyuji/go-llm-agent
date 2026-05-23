package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAutoApprover_ReturnsConfiguredDecision(t *testing.T) {
	t.Parallel()
	a := AutoApprover{Allow: true}
	d, err := a.Request(context.Background(), ApprovalRequest{RunID: "r", CallID: "c", ToolName: "shell"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed {
		t.Error("auto approver with Allow=true must return Allowed=true")
	}
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
