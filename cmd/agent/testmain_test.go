package main

import (
	"fmt"
	"os"
	"testing"
)

// TestMain テストが利用者のホームへ監査 WAL や自動メモリを書かないよう、HOME を一時ディレクトリにし IGGY_PAT を外す。
// IGGY_PAT が設定された開発機では、runOneShot を通るテストが ~/.go-llm-agent/audit-wal に run-* を残し続けるため
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	home, err := os.MkdirTemp("", "agent-test-home")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(home) }()
	if err := os.Setenv("HOME", home); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	if err := os.Unsetenv("IGGY_PAT"); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	return m.Run()
}
