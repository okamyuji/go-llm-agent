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

func TestFSRead_ExecuteSuccess_MarksRegistryKnown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 1024, nil, reg)
	if _, err := r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`)); err != nil {
		t.Fatal(err)
	}
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","old_string":"hello","new_string":"hi"}`))
	if res.IsError {
		t.Fatalf("registry should be known after fs_read Execute: %+v", res)
	}
}

func TestFSRead_ReadForSummary_DoesNotMarkRegistryKnown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 1024, nil, reg)
	if _, err := r.ReadForSummary(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","old_string":"hello","new_string":"hi"}`))
	if !res.IsError {
		t.Fatal("ReadForSummary should not mark registry known; fs_edit should still be rejected")
	}
}

func TestFSRead_NilRegistry_ExecuteDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := tool.NewSandbox([]string{dir})
	r := tool.NewFSReadWithLogger(sb, 1024, nil, nil)
	res, err := r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestFSWrite_NilRegistry_ExecuteDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	sb := tool.NewSandbox([]string{dir})
	w := tool.NewFSWriteWithLogger(sb, nil, nil)
	res, err := w.Execute(context.Background(), json.RawMessage(`{"path":"`+target+`","content":"hi"}`))
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}
