//go:build !unix

package fsx

import (
	"fmt"
	"os"
)

// openNoFollow O_NOFOLLOW を持たない OS 向けの実装。Lstat でシンボリックリンクを拒否し、
// open 後に fd の同一性 (os.SameFile) を Lstat の結果と比較する。Lstat と open の間に
// シンボリックリンクへ差し替えられた場合は開いた実体が別ファイルになり比較に失敗するため、
// 辿った結果を受け入れない (fail closed)。新規作成 (O_CREATE で不存在) のときは
// open 後の Lstat で再検査する
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	before, lerr := os.Lstat(path)
	if lerr == nil && before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", ErrSymlink, path)
	}
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("fsx: stat %s: %w", path, err)
	}
	if lerr != nil {
		// 不存在からの新規作成。作成後のパスがシンボリックリンクでないことを確認する
		after, aerr := os.Lstat(path)
		if aerr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(after, opened) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: %s", ErrSymlink, path)
		}
		return f, nil
	}
	if !os.SameFile(before, opened) {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s (replaced between lstat and open)", ErrSymlink, path)
	}
	return f, nil
}
