package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
)

func TestMarkerWriter_ClosesNotifyOnceMarkerAppears(t *testing.T) {
	m := newMarkerWriter("DONE")
	if _, err := m.Write([]byte("DO")); err != nil {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-m.notify:
		t.Fatal("marker 未出現で notify が閉じた")
	default:
	}
	if _, err := m.Write([]byte("NE!")); err != nil {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-m.notify:
	case <-time.After(time.Second):
		t.Fatal("marker 出現後も notify が閉じない")
	}
	// 2 回目以降の Write が close を重複させない (panic しない)
	if _, err := m.Write([]byte("DONE")); err != nil {
		t.Fatalf("err=%v", err)
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	if fileExists(filepath.Join(dir, "missing")) {
		t.Fatal("存在しないパスで true")
	}
	p := filepath.Join(dir, "x")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("err=%v", err)
	}
	if !fileExists(p) {
		t.Fatal("存在するパスで false")
	}
}

func TestStub_BodiesContain(t *testing.T) {
	s := &stub{bodies: []string{`{"messages":[{"role":"user"}]}`}}
	if !s.bodiesContain(`"role":"user"`) {
		t.Fatal("含む文字列を検出できていない")
	}
	if s.bodiesContain("absent") {
		t.Fatal("含まない文字列で true")
	}
}

func TestStub_CallCountStartsAtZero(t *testing.T) {
	if got := (&stub{}).callCount(); got != 0 {
		t.Fatalf("got=%d", got)
	}
}

func TestFailingDecider_ReturnsError(t *testing.T) {
	allowed, reason, err := failingDecider{}.Decide(context.Background(), agent.ApprovalRequest{ToolName: "fs_write"})
	if err == nil || allowed || reason != "" {
		t.Fatalf("allowed=%v reason=%q err=%v", allowed, reason, err)
	}
}

func TestRun_AllKeysEmitted(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), t.TempDir(), &out); err != nil {
		t.Fatalf("run err=%v", err)
	}
	for _, key := range []string{
		"approval_yes_writes_file=true",
		"approval_shows_diff=true",
		"approval_no_skips_write=true",
		"approval_summary_keeps_registry_clean=true",
		"approval_timeout_denies=true",
		"approval_fatal_error_aborts_turn=true",
	} {
		if !strings.Contains(out.String(), key) {
			t.Fatalf("missing %q in %q", key, out.String())
		}
	}
}
