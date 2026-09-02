//go:build windows

package audit

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockNB Windows には flock が無いので LockFileEx で先頭 1 バイトを排他ロックする。
// ハンドルが閉じれば (プロセスが死ねば) OS がロックを外す点は flock と同じ
func lockNB(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(windows.Overlapped))
}
