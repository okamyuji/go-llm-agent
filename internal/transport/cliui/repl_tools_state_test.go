package cliui_test

import (
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// TestREPL_ToolsWithoutArgShowsCurrentState 引数なしの /tools は現在の状態を表示する。
// 表示は tool_choice の有無から導くため、両方の状態で確認する
func TestREPL_ToolsWithoutArgShowsCurrentState(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		avoid string
	}{
		{"既定は on", "/tools\n/quit\n", "[tools] 現在: on", "現在: off"},
		{"/tools off の後は off", "/tools off\n/tools\n/quit\n", "[tools] 現在: off", "現在: on"},
		{"/tools on で戻る", "/tools off\n/tools on\n/tools\n/quit\n", "[tools] 現在: on", "現在: off"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runSlashREPL(t, &inputCapturingSvc{}, cliui.Options{}, tt.input)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("%q が無い: %q", tt.want, got)
			}
			if strings.Contains(got, tt.avoid) {
				t.Fatalf("%q を表示している: %q", tt.avoid, got)
			}
		})
	}
}
