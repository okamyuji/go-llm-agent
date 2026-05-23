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
	ap := agent.NewHTTPApprover(true)
	resultCh := make(chan agent.ApprovalDecision, 1)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d, err := ap.Request(ctx, agent.ApprovalRequest{RunID: "R", CallID: "C1", ToolName: "shell"})
		if err != nil {
			fmt.Fprintln(os.Stderr, "request err:", err)
			os.Exit(1)
		}
		resultCh <- d
	}()

	time.Sleep(60 * time.Millisecond)
	if !ap.Submit(agent.ApprovalDecision{RunID: "R", CallID: "C1", Allowed: true, Reviewer: "tester"}) {
		fmt.Fprintln(os.Stderr, "Submit failed")
		os.Exit(2)
	}
	d := <-resultCh
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
