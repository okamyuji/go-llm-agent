//go:build darwin || linux

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHookRunner_RunPre_HookTimeoutStopsDescendants(t *testing.T) {
	t.Parallel()
	lateWrite := filepath.Join(t.TempDir(), "late-write")
	allowed, _ := preRunner(HookSpec{
		Matcher: "*",
		Command: "(sleep 1; touch " + lateWrite + ") & wait",
		Timeout: 100 * time.Millisecond,
	}).RunPre(context.Background(), "shell", json.RawMessage(`{}`))
	if !allowed {
		t.Fatal("hook 自身の timeout は許可へ倒す期待")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(lateWrite); err == nil {
		t.Fatal("timeout 後に hook の子 process が副作用を実行した")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
