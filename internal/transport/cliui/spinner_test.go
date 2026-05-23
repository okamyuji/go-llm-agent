package cliui_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// safeBuffer Spinner の描画 goroutine と test の主 goroutine が共有する
// バッファを mutex で保護する。race 検出器対策
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *safeBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// TestSpinner_Disabled_WhenOutIsNotTTY bytes.Buffer は非 TTY なので
// Start/SetPhase/Stop すべて no-op で出力ゼロになる
func TestSpinner_Disabled_WhenOutIsNotTTY(t *testing.T) {
	var buf bytes.Buffer
	s := cliui.NewSpinner(cliui.SpinnerOptions{
		Out: &buf,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	s.Start(cliui.PhaseThinking, "")
	s.SetPhase(cliui.PhaseTool, "fs_read")
	s.Stop()
	if buf.Len() != 0 {
		t.Fatalf("expected no output for non-TTY, got %q", buf.String())
	}
}

// TestSpinner_Enabled_RendersFrameWithModelAndElapsed Enabled=true 時に
// モデル名と thinking フェーズと \r が描画される
func TestSpinner_Enabled_RendersFrameWithModelAndElapsed(t *testing.T) {
	buf := &safeBuffer{}
	enabled := true
	base := time.Unix(1700000000, 0)
	var calls int64
	var mu sync.Mutex
	nowFn := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return base.Add(time.Duration(calls-1) * 100 * time.Millisecond)
	}
	s := cliui.NewSpinner(cliui.SpinnerOptions{
		Out:      buf,
		Enabled:  &enabled,
		Model:    "gemini/gemini-2.5-pro",
		Now:      nowFn,
		Interval: 5 * time.Millisecond,
		Frames:   []string{"A", "B"},
	})
	s.Start(cliui.PhaseThinking, "")
	time.Sleep(40 * time.Millisecond)
	s.Stop()

	got := buf.String()
	if !strings.Contains(got, "gemini/gemini-2.5-pro") {
		t.Errorf("expected model in output, got %q", got)
	}
	if !strings.Contains(got, "thinking") {
		t.Errorf("expected thinking phase, got %q", got)
	}
	if !strings.Contains(got, "\r") {
		t.Errorf("expected carriage return, got %q", got)
	}
}
