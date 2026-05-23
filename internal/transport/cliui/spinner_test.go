package cliui_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

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
