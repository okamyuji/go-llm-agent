package cliui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// fallbackIDPattern newSessionID のフォールバック ID (ナノ秒 9 桁サフィックス)
var fallbackIDPattern = regexp.MustCompile(`^\d{8}T\d{6}Z-\d{9}$`)

// sessionIDBase newSessionID が使うタイムスタンプ表記
func sessionIDBase() string { return time.Now().UTC().Format("20060102T150405Z") }

// withCollisions dir へ base.jsonl と base-2..base-n.jsonl を作り newSessionID を呼ぶ。
// base は秒精度なので、ファイル作成中に秒が跨ぐと衝突が成立しない。跨いだ場合は
// 新しい TempDir で最初からやり直す
func withCollisions(t *testing.T, upTo int) string {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		// 秒の変わり目直後から始め、作成中に跨ぐ確率を下げる
		start := sessionIDBase()
		for sessionIDBase() == start {
			time.Sleep(2 * time.Millisecond)
		}
		base := sessionIDBase()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, base+".jsonl"), nil, 0o600); err != nil {
			t.Fatalf("write err=%v", err)
		}
		for i := 2; i <= upTo; i++ {
			name := fmt.Sprintf("%s-%d.jsonl", base, i)
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
				t.Fatalf("write err=%v", err)
			}
		}
		got := newSessionID(dir)
		if sessionIDBase() == base {
			return got
		}
	}
	t.Fatal("秒を跨がずにファイルを準備できなかった")
	return ""
}

// TestNewSessionID_SkipsExistingSuffixes 衝突している連番を飛ばして次の番号を返す。
// i-- 変異は base-1 のように番号が逆行するため、正確な番号の検証で落ちる
func TestNewSessionID_SkipsExistingSuffixes(t *testing.T) {
	got := withCollisions(t, 2)
	want := got[:len(sessionIDBase())] // base 部分は呼び出し時刻依存なので結果から取る
	if got != want+"-3" {
		t.Fatalf("newSessionID=%q, want %q", got, want+"-3")
	}
}

// TestNewSessionID_UsesLastAttemptBeforeGivingUp 試行上限の直前 (base-997) までは
// 連番を返す。上限式の +2 を -2 にする変異はここでフォールバックへ落ちる
func TestNewSessionID_UsesLastAttemptBeforeGivingUp(t *testing.T) {
	got := withCollisions(t, maxSessionIDAttempts-4)
	if fallbackIDPattern.MatchString(got) {
		t.Fatalf("newSessionID=%q, want 連番 (フォールバックではない)", got)
	}
	want := fmt.Sprintf("%s-%d", got[:len(sessionIDBase())], maxSessionIDAttempts-3)
	if got != want {
		t.Fatalf("newSessionID=%q, want %q", got, want)
	}
}

// TestNewSessionID_ExhaustsAttemptsReturnsFallback 上限回数を使い切るとフォールバックを返す。
// 境界を <= にする変異は 1 回多く試行し base-1001 を返すため落ちる
func TestNewSessionID_ExhaustsAttemptsReturnsFallback(t *testing.T) {
	got := withCollisions(t, maxSessionIDAttempts)
	if !fallbackIDPattern.MatchString(got) {
		t.Fatalf("newSessionID=%q, want ナノ秒サフィックスのフォールバック", got)
	}
}
