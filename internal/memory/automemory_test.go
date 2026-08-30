package memory_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/memory"
)

// newStore テスト用 Store を TempDir 上に作るヘルパ
func newStore(t *testing.T) (*memory.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := memory.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, dir
}

func TestStore_WriteAndRead(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Write("topic.md", "hello", false); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := s.Read("topic.md", 1<<20)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "topic.md"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%v, want 0600", fi.Mode().Perm())
	}
}

func TestStore_WriteOverwriteAndAppend(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Write("t.md", "one\n", false); err != nil {
		t.Fatalf("Write1: %v", err)
	}
	if err := s.Write("t.md", "two\n", true); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ := s.Read("t.md", 1<<20)
	if got != "one\ntwo\n" {
		t.Fatalf("append 結果 %q", got)
	}
	if err := s.Write("t.md", "three\n", false); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _ = s.Read("t.md", 1<<20)
	if got != "three\n" {
		t.Fatalf("overwrite 結果 %q", got)
	}
}

func TestStore_RejectsUnsafePaths(t *testing.T) {
	s, _ := newStore(t)
	cases := []string{"../escape.md", "/etc/passwd.md", "a/../../b.md", "note.txt", "", "."}
	for _, rel := range cases {
		if err := s.Write(rel, "x", false); err == nil {
			t.Errorf("Write(%q) がエラーにならない", rel)
		}
		if _, err := s.Read(rel, 100); err == nil {
			t.Errorf("Read(%q) がエラーにならない", rel)
		}
	}
}

func TestStore_RejectsSymlinkTarget(t *testing.T) {
	s, dir := newStore(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("prep: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := s.Read("link.md", 100); err == nil {
		t.Fatalf("シンボリックリンクの Read がエラーにならない")
	}
	if err := s.Write("link.md", "x", false); err == nil {
		t.Fatalf("シンボリックリンクへの Write がエラーにならない")
	}
}

func TestStore_WriteSizeLimit(t *testing.T) {
	s, _ := newStore(t)
	big := strings.Repeat("a", 1<<20+1)
	if err := s.Write("big.md", big, false); err == nil {
		t.Fatalf("1MiB 超の Write がエラーにならない")
	}
	ok := strings.Repeat("a", 1<<20)
	if err := s.Write("ok.md", ok, false); err != nil {
		t.Fatalf("1MiB ちょうどが拒否された: %v", err)
	}
}

func TestStore_ReadIndex(t *testing.T) {
	s, _ := newStore(t)
	// 索引なしは空文字
	got, err := s.ReadIndex(200, 1<<20)
	if err != nil {
		t.Fatalf("ReadIndex empty: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}

	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("- line\n")
	}
	if err := s.Write("MEMORY.md", b.String(), false); err != nil {
		t.Fatalf("Write index: %v", err)
	}

	got, err = s.ReadIndex(3, 1<<20)
	if err != nil {
		t.Fatalf("ReadIndex lines: %v", err)
	}
	if strings.Count(got, "\n") > 3 {
		t.Fatalf("行数上限が効いていない: %q", got)
	}

	got, err = s.ReadIndex(200, 10)
	if err != nil {
		t.Fatalf("ReadIndex bytes: %v", err)
	}
	if len(got) > 10 {
		t.Fatalf("バイト上限が効いていない: %d bytes", len(got))
	}
}

func TestStore_ReadIndexRuneBoundary(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Write("MEMORY.md", "ああ", false); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := s.ReadIndex(200, 4)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if got != "あ" {
		t.Fatalf("rune 境界で切れていない: %q", got)
	}
}

func TestStore_List(t *testing.T) {
	s, _ := newStore(t)
	for _, name := range []string{"b.md", "a.md", "MEMORY.md"} {
		if err := s.Write(name, "x", false); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"MEMORY.md", "a.md", "b.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("List=%v, want %v", got, want)
	}
}

func TestProjectKey_PlainRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("prep: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("prep sub: %v", err)
	}
	keyRoot := memory.ProjectKey(dir)
	keySub := memory.ProjectKey(sub)
	if keyRoot != keySub {
		t.Fatalf("サブディレクトリでキーが変わる: %q vs %q", keyRoot, keySub)
	}
	if strings.ContainsRune(keyRoot, filepath.Separator) {
		t.Fatalf("キーにパス区切りが残る: %q", keyRoot)
	}
}

func TestProjectKey_WorktreeGitFile(t *testing.T) {
	main := t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, ".git", "worktrees", "wt"), 0o755); err != nil {
		t.Fatalf("prep: %v", err)
	}
	wt := t.TempDir()
	gitFile := "gitdir: " + filepath.Join(main, ".git", "worktrees", "wt") + "\n"
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte(gitFile), 0o644); err != nil {
		t.Fatalf("prep gitfile: %v", err)
	}
	if got, want := memory.ProjectKey(wt), memory.ProjectKey(main); got != want {
		t.Fatalf("worktree キー %q, want %q", got, want)
	}
}

func TestProjectKey_NonGitUsesCwd(t *testing.T) {
	dir := t.TempDir()
	key := memory.ProjectKey(dir)
	if key == "" {
		t.Fatalf("空キー")
	}
	if strings.ContainsRune(key, filepath.Separator) {
		t.Fatalf("キーにパス区切りが残る: %q", key)
	}
}
