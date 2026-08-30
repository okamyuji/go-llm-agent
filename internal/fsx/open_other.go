//go:build !unix

package fsx

import "os"

// openNoFollow O_NOFOLLOW を持たない OS では Lstat と開いた fd の同一性比較で代替する
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return openWithIdentityCheck(path, flag, perm)
}
