//go:build unix

package audit

import (
	"os"
	"syscall"
)

// lockNB 排他ロック。取れなければ待たずにエラー
func lockNB(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
