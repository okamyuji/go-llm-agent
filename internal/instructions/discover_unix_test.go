//go:build unix

package instructions_test

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/instructions"
)

func TestDiscover_NonRegularFileSkipped(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "AGENTS.md")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	srcs, err := instructions.Discover("", root, []string{root}, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 0 {
		t.Fatalf("FIFO が読まれた: %+v", srcs)
	}
}
