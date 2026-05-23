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
// 初回描画でモデル名と thinking フェーズと \r が出る。Sleep は使わず
// Start 直後の同期描画と Stop の同期終了に依拠する
func TestSpinner_Enabled_RendersFrameWithModelAndElapsed(t *testing.T) {
	buf := &safeBuffer{}
	enabled := true
	base := time.Unix(1700000000, 0)
	s := cliui.NewSpinner(cliui.SpinnerOptions{
		Out:      buf,
		Enabled:  &enabled,
		Model:    "gemini/gemini-2.5-pro",
		Now:      func() time.Time { return base },
		Interval: 1 * time.Hour, // ticker を発火させない
		Frames:   []string{"A", "B"},
	})
	s.Start(cliui.PhaseThinking, "")
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

// TestSpinner_SetPhase_SwitchesToToolLabel SetPhase で tool ラベルに切替わり、
// Stop で末尾が消去シーケンスで終わる。Sleep は使わず synchronization channel で
// SetPhase の処理完了を待つ
func TestSpinner_SetPhase_SwitchesToToolLabel(t *testing.T) {
	buf := &safeBuffer{}
	enabled := true
	base := time.Unix(1700000000, 0)
	renderCh := make(chan struct{}, 8)
	s := cliui.NewSpinner(cliui.SpinnerOptions{
		Out:        buf,
		Enabled:    &enabled,
		Model:      "M",
		Now:        func() time.Time { return base },
		Interval:   1 * time.Hour, // ticker を発火させない
		Frames:     []string{"x"},
		OnRendered: func() { renderCh <- struct{}{} },
	})
	s.Start(cliui.PhaseThinking, "")
	<-renderCh // 初回描画完了
	s.SetPhase(cliui.PhaseTool, "fs_read")
	<-renderCh // SetPhase 後の再描画完了
	s.Stop()

	got := buf.String()
	if !strings.Contains(got, "tool: fs_read") {
		t.Errorf("expected tool label, got %q", got)
	}
	if !strings.HasSuffix(got, "\r\x1b[K") {
		t.Errorf("expected output to end with clear sequence, got %q", got)
	}
}

// TestSpinner_Stop_Idempotent Stop を 2 回呼んでも panic / leak しない
func TestSpinner_Stop_Idempotent(t *testing.T) {
	buf := &safeBuffer{}
	enabled := true
	s := cliui.NewSpinner(cliui.SpinnerOptions{
		Out:      buf,
		Enabled:  &enabled,
		Now:      time.Now,
		Interval: 5 * time.Millisecond,
		Frames:   []string{"x"},
	})
	s.Start(cliui.PhaseThinking, "")
	s.Stop()
	s.Stop()
}

// TestSpinner_RestartAfterStop Stop 後に再度 Start できる
func TestSpinner_RestartAfterStop(t *testing.T) {
	buf := &safeBuffer{}
	enabled := true
	s := cliui.NewSpinner(cliui.SpinnerOptions{
		Out:      buf,
		Enabled:  &enabled,
		Model:    "M",
		Now:      func() time.Time { return time.Unix(1700000000, 0) },
		Interval: 1 * time.Hour, // ticker を発火させない
		Frames:   []string{"x"},
	})
	s.Start(cliui.PhaseThinking, "")
	s.Stop()
	s.Start(cliui.PhaseTool, "shell")
	s.Stop()
	got := buf.String()
	if !strings.Contains(got, "tool: shell") {
		t.Errorf("expected re-start with tool label, got %q", got)
	}
}
