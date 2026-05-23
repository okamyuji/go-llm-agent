// Package main HTTP Approver の Submit/Request サイクルを確認するフィクスチャ
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
)

func main() {
	ap := agent.NewHTTPApprover()
	resultCh := make(chan agent.ApprovalDecision, 1)
	errCh := make(chan error, 1)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d, err := ap.Request(ctx, agent.ApprovalRequest{RunID: "R", CallID: "C1", ToolName: "shell"})
		if err != nil {
			// goroutine 内で os.Exit すると defer が走らず main の cleanup 経路も飛ばすため、
			// エラーは channel で main に伝えてから return する
			errCh <- err
			return
		}
		resultCh <- d
	}()

	// Request の内部 channel 登録が終わるまで Submit が失敗するため、
	// 短い間隔でリトライして race を避ける
	deadline := time.Now().Add(2 * time.Second)
	submitted := false
	for time.Now().Before(deadline) {
		if ap.Submit(agent.ApprovalDecision{RunID: "R", CallID: "C1", Allowed: true, Reviewer: "tester"}) {
			submitted = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !submitted {
		fmt.Fprintln(os.Stderr, "Submit failed after retries")
		os.Exit(2)
	}
	var d agent.ApprovalDecision
	select {
	case d = <-resultCh:
	case err := <-errCh:
		fmt.Fprintln(os.Stderr, "request err:", err)
		os.Exit(1)
	}
	if !d.Allowed {
		fmt.Fprintln(os.Stderr, "expected allowed")
		os.Exit(3)
	}
	fmt.Printf("approval_allowed=%v reviewer=%s\n", d.Allowed, d.Reviewer)

	// timeout シナリオ
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	d2, _ := ap.Request(ctx2, agent.ApprovalRequest{RunID: "R", CallID: "C2", ToolName: "shell"})
	fmt.Printf("timeout_allowed=%v\n", d2.Allowed)
}
