package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenWithIdentityCheck_RegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.md")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openWithIdentityCheck(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("openWithIdentityCheck: %v", err)
	}
	_ = f.Close()
}

func TestOpenWithIdentityCheck_CreatesNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.md")
	f, err := openWithIdentityCheck(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("openWithIdentityCheck: %v", err)
	}
	_ = f.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("作成されていない: %v", err)
	}
}

func TestOpenWithIdentityCheck_RejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.md")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	_, err := openWithIdentityCheck(link, os.O_RDONLY, 0)
	if !errors.Is(err, ErrSymlink) {
		t.Fatalf("シンボリックリンクを拒否していない: %v", err)
	}
}

func TestOpenWithIdentityCheck_MissingWithoutCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.md")
	if _, err := openWithIdentityCheck(path, os.O_RDONLY, 0); err == nil {
		t.Fatalf("不存在ファイルでエラーにならない")
	}
}

func TestVerifyIdentity_DetectsReplacement(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.md")
	b := filepath.Join(dir, "b.md")
	if err := os.WriteFile(a, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeA, err := os.Lstat(a)
	if err != nil {
		t.Fatal(err)
	}
	// Lstat で見たのは a だが、開いたのは b (差し替えを模擬)
	f, err := os.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if verr := verifyIdentity(f, a, beforeA); !errors.Is(verr, ErrSymlink) {
		t.Fatalf("差し替えを検出していない: %v", verr)
	}
}

func TestVerifyCreated_RejectsSymlinkAtPath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.md")
	if err := os.WriteFile(real, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	f, err := os.Open(real)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if verr := verifyCreated(link, opened); !errors.Is(verr, ErrSymlink) {
		t.Fatalf("作成後パスのシンボリックリンクを検出していない: %v", verr)
	}
}
