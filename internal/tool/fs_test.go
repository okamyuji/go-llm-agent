package tool_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func TestFSRead_AllowedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := tool.NewSandbox([]string{dir})
	r := tool.NewFSRead(sb, 1024)
	res, err := r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.IsError {
		t.Fatalf("error result: %s", res.Content)
	}
	if res.Content != "hello" {
		t.Fatalf("content=%q", res.Content)
	}
}

func TestFSRead_DeniesOutside(t *testing.T) {
	dir := t.TempDir()
	sb := tool.NewSandbox([]string{dir})
	r := tool.NewFSRead(sb, 1024)
	res, _ := r.Execute(context.Background(), json.RawMessage(`{"path":"/etc/passwd"}`))
	if !res.IsError {
		t.Fatal("外側は IsError")
	}
}

func TestFSRead_TruncatesAboveMax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := tool.NewSandbox([]string{dir})
	r := tool.NewFSRead(sb, 5)
	res, _ := r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	if !res.Truncated {
		t.Fatal("truncated")
	}
	if len(res.Content) != 5 {
		t.Fatalf("len=%d", len(res.Content))
	}
}

func TestFSRead_RejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	sb := tool.NewSandbox([]string{dir})
	r := tool.NewFSRead(sb, 1024)
	res, _ := r.Execute(context.Background(), json.RawMessage(`{"path":"`+link+`"}`))
	if !res.IsError {
		t.Fatal("symlink 経由の fs_read は拒否されるべき (TOCTOU 緩和)")
	}
}

func TestFSWrite_RejectsSymlinkOverwrite(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(real, []byte("orig"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	sb := tool.NewSandbox([]string{dir})
	w := tool.NewFSWrite(sb)
	res, _ := w.Execute(context.Background(), json.RawMessage(`{"path":"`+link+`","content":"overwrite"}`))
	if !res.IsError {
		t.Fatal("既存 symlink への fs_write は拒否されるべき (TOCTOU 緩和)")
	}
	// 元ファイルが書き換わっていないこと
	b, _ := os.ReadFile(real)
	if string(b) != "orig" {
		t.Fatalf("元ファイルが書き換わっている: %q", string(b))
	}
}

func TestFSWrite_WritesAndCreatesDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "out.txt")
	sb := tool.NewSandbox([]string{dir})
	w := tool.NewFSWrite(sb)
	res, _ := w.Execute(context.Background(), json.RawMessage(`{"path":"`+target+`","content":"hi"}`))
	if res.IsError {
		t.Fatalf("err: %s", res.Content)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hi" {
		t.Fatalf("got %q", string(b))
	}
}
