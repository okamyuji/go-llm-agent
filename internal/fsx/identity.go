package fsx

import (
	"fmt"
	"os"
)

// openWithIdentityCheck O_NOFOLLOW を使わずにシンボリックリンクを拒否する open。
// Lstat でシンボリックリンクを拒否し、open 後に fd の同一性 (os.SameFile) を Lstat の
// 結果と比較する。Lstat と open の間にシンボリックリンクへ差し替えられた場合は開いた
// 実体が別ファイルになり比較に失敗するため、辿った結果を受け入れない (fail closed)。
// 不存在からの新規作成は open 後の Lstat で再検査する。
// unix でも動作するためここに置き、ホスト OS でテストする
func openWithIdentityCheck(path string, flag int, perm os.FileMode) (*os.File, error) {
	before, lerr := os.Lstat(path)
	if lerr == nil && before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s", ErrSymlink, path)
	}
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	if verr := verifyIdentity(f, path, before); verr != nil {
		_ = f.Close()
		return nil, verr
	}
	return f, nil
}

// verifyIdentity 開いた fd が before (open 前の Lstat 結果。nil なら新規作成) と
// 同じ実体を指すことを確認する
func verifyIdentity(f *os.File, path string, before os.FileInfo) error {
	opened, err := f.Stat()
	if err != nil {
		return fmt.Errorf("fsx: stat %s: %w", path, err)
	}
	if before == nil {
		return verifyCreated(path, opened)
	}
	if !os.SameFile(before, opened) {
		return fmt.Errorf("%w: %s (replaced between lstat and open)", ErrSymlink, path)
	}
	return nil
}

// verifyCreated 新規作成したパスがシンボリックリンクでなく、開いた fd と同一であることを確認する
func verifyCreated(path string, opened os.FileInfo) error {
	after, err := os.Lstat(path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(after, opened) {
		return fmt.Errorf("%w: %s (created path is not the opened file)", ErrSymlink, path)
	}
	return nil
}
