//go:build unix

package fsx

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openNoFollow O_NOFOLLOW 付きで開く。最終コンポーネントがシンボリックリンクなら
// カーネルが ELOOP (macOS は EMLINK の場合もある) で拒否する
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, path)
		}
		return nil, err
	}
	return f, nil
}
