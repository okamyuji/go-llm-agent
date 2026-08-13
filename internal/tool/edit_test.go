package tool_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readAndEdit(sb *tool.Sandbox, reg *tool.ReadRegistry, path string) (tool.Result, tool.Result) {
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	rres, _ := r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	e := tool.NewFSEdit(sb, reg, nil)
	eres, _ := e.Execute(context.Background(), editArgs(path, "hello", "goodbye", false))
	return rres, eres
}

func editArgs(path, oldStr, newStr string, replaceAll bool) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"path": path, "old_string": oldStr, "new_string": newStr, "replace_all": replaceAll,
	})
	return b
}

func TestFSEdit_NoMatch_IsError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	if _, err := r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`)); err != nil {
		t.Fatal(err)
	}
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "nomatch", "x", false))
	if !res.IsError || !contains(res.Content, "old_string not found in") {
		t.Fatalf("got %+v", res)
	}
}

func TestFSEdit_SingleMatch_ReplacesSuccessfully(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	rres, eres := readAndEdit(sb, reg, path)
	if rres.IsError {
		t.Fatalf("read err: %+v", rres)
	}
	if eres.IsError || !contains(eres.Content, "replaced 1 occurrence(s) in") {
		t.Fatalf("got %+v", eres)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "goodbye world" {
		t.Fatalf("file content=%q", string(b))
	}
}

func TestFSEdit_ThreeMatchesReplaceAllFalse_IsError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "aa aa aa")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	_, _ = r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "aa", "bb", false))
	if !res.IsError || !contains(res.Content, "old_string matched 3 times") {
		t.Fatalf("got %+v", res)
	}
}

func TestFSEdit_ThreeMatchesReplaceAllTrue_ReplacesAll(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "aa aa aa")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	_, _ = r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "aa", "bb", true))
	if res.IsError {
		t.Fatalf("got %+v", res)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "bb bb bb" {
		t.Fatalf("file content=%q", string(b))
	}
}

func TestFSEdit_NoMatchReplaceAllTrue_IsError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	_, _ = r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "nomatch", "x", true))
	if !res.IsError || !contains(res.Content, "old_string not found in") {
		t.Fatalf("got %+v", res)
	}
}

func TestFSEdit_EmptyNewString_DeletesMatch(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	_, _ = r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "hello ", "", false))
	if res.IsError {
		t.Fatalf("got %+v", res)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "world" {
		t.Fatalf("file content=%q", string(b))
	}
}

func TestFSEdit_NilRegistry_IsErrorNoPanic(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	e := tool.NewFSEdit(sb, nil, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "hello", "hi", false))
	if !res.IsError || !contains(res.Content, "was not read in this session") {
		t.Fatalf("got %+v", res)
	}
}

func TestFSEdit_ExternalModificationAfterRead_NoMatchIsError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	_, _ = r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	// 外部プロセスによる書き換えをシミュレート
	if err := os.WriteFile(path, []byte("completely different content"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "hello", "hi", false))
	if !res.IsError || !contains(res.Content, "old_string not found in") {
		t.Fatalf("got %+v (registry不失効だが完全一致判定が誤爆を防ぐ)", res)
	}
}

func TestFSEdit_ReadForSummaryDoesNotMarkKnown(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	if _, err := r.ReadForSummary(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "hello", "hi", false))
	if !res.IsError || !contains(res.Content, "was not read in this session") {
		t.Fatalf("got %+v", res)
	}
}

func TestFSEdit_UnreadFile_IsError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "hello", "hi", false))
	if !res.IsError || !contains(res.Content, "was not read in this session") {
		t.Fatalf("got %+v", res)
	}
}

func TestFSEdit_AfterFSRead_Succeeds(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	rres, eres := readAndEdit(sb, reg, path)
	if rres.IsError || eres.IsError {
		t.Fatalf("read=%+v edit=%+v", rres, eres)
	}
}

func TestFSEdit_AfterFSWriteWithoutRead_Succeeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	w := tool.NewFSWriteWithLogger(sb, nil, reg)
	wres, _ := w.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","content":"hello world"}`))
	if wres.IsError {
		t.Fatalf("write err: %+v", wres)
	}
	e := tool.NewFSEdit(sb, reg, nil)
	eres, _ := e.Execute(context.Background(), editArgs(path, "hello", "hi", false))
	if eres.IsError {
		t.Fatalf("edit err: %+v", eres)
	}
}

func TestFSEdit_OldStringEqualsNewString_IsError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	_, _ = r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "hello", "hello", false))
	if !res.IsError || !contains(res.Content, "nothing to do") {
		t.Fatalf("got %+v", res)
	}
}

func TestFSEdit_OutsideSandbox_IsError(t *testing.T) {
	dir := t.TempDir()
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs("/etc/passwd", "root", "x", false))
	if !res.IsError {
		t.Fatal("外側は IsError")
	}
}

func TestFSEdit_DenyPatternPath_IsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, dir, filepath.Join(".git", "config"), "core")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "core", "x", false))
	if !res.IsError {
		t.Fatal(".git/config への fs_edit は拒否されるべき")
	}
}

// TestFSEdit_SymlinkTarget_IsError read registry は失効機構を持たない (03-fs-edit.md
// 「mtime / size による失効機構は設けない」) ため、fs_read で既読マークした後に
// 同じパスが symlink に差し替わる TOCTOU シナリオを再現する。fs_read 自体は symlink を
// 拒否するため、symlink はレジストリ登録後に外部から作られたものとして扱う
func TestFSEdit_SymlinkTarget_IsError(t *testing.T) {
	dir := t.TempDir()
	other := writeFile(t, dir, "other.txt", "elsewhere")
	path := writeFile(t, dir, "a.txt", "hello world")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	if _, err := r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`)); err != nil {
		t.Fatal(err)
	}
	// 既読マーク後、同じパスを symlink に差し替える (registry は失効しない)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, path); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "hello", "hi", false))
	if !res.IsError || !contains(res.Content, "symlink 経由のアクセスは拒否") {
		t.Fatalf("got %+v", res)
	}
}

func TestFSEdit_MultibyteContent_ReplacesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "こんにちは、世界")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
	_, _ = r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "こんにちは", "さようなら", false))
	if res.IsError {
		t.Fatalf("got %+v", res)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "さようなら、世界" {
		t.Fatalf("file content=%q", string(b))
	}
}

func TestFSEdit_ConcurrentRegistryAccess_NoRace(t *testing.T) {
	dir := t.TempDir()
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			path := writeFile(t, dir, filepath.Base(filepath.Join(dir, "f"+string(rune('a'+n))+".txt")), "x")
			r := tool.NewFSReadWithLogger(sb, 0, nil, reg)
			_, _ = r.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
			e := tool.NewFSEdit(sb, reg, nil)
			_, _ = e.Execute(context.Background(), editArgs(path, "x", "y", false))
		}(i)
	}
	wg.Wait()
}

func TestFSEdit_EmptyPath_IsError(t *testing.T) {
	sb := tool.NewSandbox([]string{"/tmp"})
	reg := tool.NewReadRegistry()
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs("", "a", "b", false))
	if !res.IsError || !contains(res.Content, "path is required") {
		t.Fatalf("got %+v", res)
	}
}

func TestFSEdit_EmptyOldString_IsError(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "a.txt", "hello")
	sb := tool.NewSandbox([]string{dir})
	reg := tool.NewReadRegistry()
	e := tool.NewFSEdit(sb, reg, nil)
	res, _ := e.Execute(context.Background(), editArgs(path, "", "b", false))
	if !res.IsError || !contains(res.Content, "old_string is required") {
		t.Fatalf("got %+v", res)
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
