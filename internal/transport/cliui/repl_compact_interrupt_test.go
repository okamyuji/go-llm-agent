package cliui_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// runCompactWithSingleByte 圧縮開始を検知してから b を 1 バイトだけ流し、
// 直後に入力を閉じる。圧縮中に届くバイトを 1 つに限定しないと、後続の
// 打鍵が中断判定を肩代わりしてしまい、判定式の誤りを検出できない
func runCompactWithSingleByte(t *testing.T, b byte) string {
	t.Helper()
	prov := &compactProv{summary: "要約", delay: 400 * time.Millisecond}
	opts := autoOptions()
	opts.TriggerRatio = 1.0 // 自動発火させず /compact だけで起動する
	pr, pw := io.Pipe()
	out := newMarkerWriter("[compact] 会話履歴を圧縮しています")
	r := cliui.NewREPL(&usageSvc{usages: [][]int{{10}, {10}}}, cliui.Options{
		Model:          "fake/m",
		In:             pr,
		Out:            out,
		DisableSpinner: true,
		Registry:       compactReg{p: prov},
		Compaction:     opts,
	})
	go func() {
		_, _ = pw.Write([]byte("q1\nq2\n/compact\n"))
		select {
		case <-out.ch:
		case <-time.After(3 * time.Second):
		}
		_, _ = pw.Write([]byte{b})
		_ = pw.Close()
	}()
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = pr.Close()
	return out.String()
}

// TestREPL_CompactionInterruptKeys 圧縮中の打鍵は ESC と Ctrl-C だけが中断で、
// それ以外は読み捨てて圧縮を完了する
func TestREPL_CompactionInterruptKeys(t *testing.T) {
	tests := []struct {
		name      string
		key       byte
		interrupt bool
	}{
		{"ESC は中断する", 0x1b, true},
		{"Ctrl-C は中断する", 0x03, true},
		{"通常バイトは読み捨てる", 'x', false},
		{"Ctrl-D は中断しない", 0x04, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runCompactWithSingleByte(t, tt.key)
			interrupted := strings.Contains(got, "[compact] 中断しました。元の履歴のまま続行します")
			compacted := strings.Contains(got, "[compact] 会話履歴を圧縮しました")
			if interrupted != tt.interrupt {
				t.Fatalf("中断表示=%v, want %v: %q", interrupted, tt.interrupt, got)
			}
			if compacted == tt.interrupt {
				t.Fatalf("圧縮完了表示=%v, want %v: %q", compacted, !tt.interrupt, got)
			}
		})
	}
}
