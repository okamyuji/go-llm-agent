package cliui_test

import (
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// initialCompactTimeout パッケージ初期化直後の値。個別テストが
// SetCompactTimeoutForTest で差し替えるため、init 時点で捕まえる
var initialCompactTimeout = cliui.CompactTimeoutForTest()

// TestCompactTimeout_Default 既定の圧縮上限は 60 秒。短すぎると通常の要約が
// 常に打ち切られ、長すぎると中断できない待ちが生じる
func TestCompactTimeout_Default(t *testing.T) {
	if initialCompactTimeout != 60*time.Second {
		t.Fatalf("compactTimeout=%v, want %v", initialCompactTimeout, 60*time.Second)
	}
}
