// Package fsx シンボリックリンクを辿らない安全な open を提供する。
// 事前の Lstat 検査と open の間でファイルが差し替えられる TOCTOU を、
// unix では O_NOFOLLOW で、それ以外の OS では Lstat と開いた fd の同一性比較で塞ぐ
package fsx

import (
	"errors"
	"fmt"
	"os"
)

// ErrNotRegular open したパスが通常ファイルでない (ディレクトリ・デバイス等) 場合に返す
var ErrNotRegular = errors.New("fsx: not a regular file")

// ErrSymlink open したパスがシンボリックリンクだった場合に返す
var ErrSymlink = errors.New("fsx: symlink is not allowed")

// OpenNoFollow path をシンボリックリンクを辿らずに開き、開いた fd が通常ファイルで
// あることを確認して返す。最終コンポーネントがシンボリックリンクなら開かない
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := openNoFollow(path, flag, perm)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("fsx: stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	return f, nil
}
