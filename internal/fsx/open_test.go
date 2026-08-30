package fsx_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/fsx"
)

func TestOpenNoFollow_RegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := fsx.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenNoFollow: %v", err)
	}
	_ = f.Close()
}

func TestOpenNoFollow_CreatesWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.txt")
	f, err := fsx.OpenNoFollow(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenNoFollow: %v", err)
	}
	_ = f.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ファイルが作成されていない: %v", err)
	}
}

func TestOpenNoFollow_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := fsx.OpenNoFollow(link, os.O_RDONLY, 0); err == nil {
		t.Fatalf("シンボリックリンクが開けてしまった")
	}
}

func TestOpenNoFollow_RejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := fsx.OpenNoFollow(dir, os.O_RDONLY, 0)
	if err == nil {
		t.Fatalf("ディレクトリが開けてしまった")
	}
	if !errors.Is(err, fsx.ErrNotRegular) && !os.IsNotExist(err) {
		// unix では O_NOFOLLOW でもディレクトリは開けるため ErrNotRegular になる
		t.Logf("err=%v", err)
	}
}
