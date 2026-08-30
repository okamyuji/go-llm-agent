// Package fsx シンボリックリンクを辿らない安全な open を提供する。
// 事前の Lstat 検査と open の間でファイルが差し替えられる TOCTOU を、
// O_NOFOLLOW (unix) と open 後の fd に対する Stat 再検査で塞ぐ
package fsx

import (
	"errors"
	"fmt"
	"os"
)

// ErrNotRegular open したパスが通常ファイルでない (ディレクトリ・デバイス等) 場合に返す
var ErrNotRegular = errors.New("fsx: not a regular file")

// OpenNoFollow path をシンボリックリンクを辿らずに開き、開いた fd が通常ファイルで
// あることを確認して返す。シンボリックリンクは unix では open 自体が失敗し、
// それ以外の OS でも Stat 再検査で通常ファイル以外を拒否する
func OpenNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, flag|noFollowFlag, perm)
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
