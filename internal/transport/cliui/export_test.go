package cliui

import "time"

// SetCompactTimeoutForTest 圧縮の上限時間を差し替え、復元関数を返す。
// テスト専用 (export_test.go はビルド対象に含まれない)
func SetCompactTimeoutForTest(d time.Duration) func() {
	prev := compactTimeout
	compactTimeout = d
	return func() { compactTimeout = prev }
}
