package memory_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	for range 10 {
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

	// 行数上限 0 は無制限
	got, err = s.ReadIndex(0, 1<<20)
	if err != nil {
		t.Fatalf("ReadIndex unlimited lines: %v", err)
	}
	if got != b.String() {
		t.Fatalf("行数上限 0 が無制限になっていない: %q", got)
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

// prepWorktree git 2.50 が `git worktree add` で作るレイアウトを再現する。
// wt/.git は gitdir: で main/.git/worktrees/wt を指し、そこにある gitdir ファイルが
// wt/.git の絶対パスで逆参照する
func prepWorktree(t *testing.T, main, wt string, withBackRef bool) {
	t.Helper()
	gitdir := filepath.Join(main, ".git", "worktrees", "wt")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatalf("prep: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatalf("prep gitfile: %v", err)
	}
	if withBackRef {
		if err := os.WriteFile(filepath.Join(gitdir, "gitdir"), []byte(filepath.Join(wt, ".git")+"\n"), 0o644); err != nil {
			t.Fatalf("prep backref: %v", err)
		}
	}
}

func TestProjectKey_WorktreeGitFile(t *testing.T) {
	main := t.TempDir()
	wt := t.TempDir()
	prepWorktree(t, main, wt, true)
	if got, want := memory.ProjectKey(wt), memory.ProjectKey(main); got != want {
		t.Fatalf("worktree キー %q, want %q", got, want)
	}
}

func TestProjectKey_ForgedGitFileWithoutBackRefFallsBack(t *testing.T) {
	victim := t.TempDir()
	attacker := t.TempDir()
	// 逆参照の無い gitdir: は偽造とみなし、被害者のキーへ寄せない
	prepWorktree(t, victim, attacker, false)
	got := memory.ProjectKey(attacker)
	if got == memory.ProjectKey(victim) {
		t.Fatalf("偽造 .git ファイルで他プロジェクトのキーになった: %q", got)
	}
	if !strings.HasPrefix(got, filepath.Base(attacker)+"-") {
		t.Fatalf("偽造時は自ディレクトリをキーにするべき: %q", got)
	}
}

func TestProjectKey_WorktreeBackRefPointingElsewhereRejected(t *testing.T) {
	victim := t.TempDir()
	realWT := t.TempDir()
	attacker := t.TempDir()
	prepWorktree(t, victim, realWT, true)
	// 攻撃者は本物の worktree 用 gitdir を指すが、逆参照は realWT を指しているため不一致
	gitdir := filepath.Join(victim, ".git", "worktrees", "wt")
	if err := os.WriteFile(filepath.Join(attacker, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatalf("prep: %v", err)
	}
	if memory.ProjectKey(attacker) == memory.ProjectKey(victim) {
		t.Fatalf("逆参照が別ディレクトリを指す gitdir を受け入れた")
	}
}

func TestSanitizeKey_DistinguishesSeparatorCollisions(t *testing.T) {
	a := t.TempDir()
	if err := os.MkdirAll(filepath.Join(a, "work", "a-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a, "work", "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	k1 := memory.ProjectKey(filepath.Join(a, "work", "a-b"))
	k2 := memory.ProjectKey(filepath.Join(a, "work", "a", "b"))
	if k1 == k2 {
		t.Fatalf("/work/a-b と /work/a/b が同じキーになった: %q", k1)
	}
	if !strings.HasPrefix(k1, "a-b-") || !strings.HasPrefix(k2, "b-") {
		t.Fatalf("basename プレフィックスが無い: %q %q", k1, k2)
	}
}

func TestStore_RejectedOverwriteKeepsPriorContent(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Write("keep.md", "precious", false); err != nil {
		t.Fatalf("prep: %v", err)
	}
	err := s.Write("keep.md", strings.Repeat("x", 1<<20+1), false)
	if !errors.Is(err, memory.ErrMemoryFileTooLarge) {
		t.Fatalf("上限超過が ErrMemoryFileTooLarge でない: %v", err)
	}
	got, rerr := s.Read("keep.md", 1<<20)
	if rerr != nil || got != "precious" {
		t.Fatalf("拒否された上書きで既存内容が失われた: %q err=%v", got, rerr)
	}
}

func TestProjectKey_SubmoduleRelativeGitFile(t *testing.T) {
	main := t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, ".git", "modules", "sub"), 0o755); err != nil {
		t.Fatalf("prep: %v", err)
	}
	sub := filepath.Join(main, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("prep sub: %v", err)
	}
	// git 2.50 の実レイアウト: submodule の .git は相対の gitdir: を書き、
	// modules/sub/config の core.worktree が gitdir 基準の相対パスで sub を指す
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644); err != nil {
		t.Fatalf("prep gitfile: %v", err)
	}
	cfg := "[core]\n\tbare = false\n\tworktree = ../../../sub\n"
	if err := os.WriteFile(filepath.Join(main, ".git", "modules", "sub", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("prep config: %v", err)
	}
	got := memory.ProjectKey(sub)
	if got != memory.ProjectKey(main) {
		t.Fatalf("submodule キー %q, want 主リポジトリと同じ %q", got, memory.ProjectKey(main))
	}
	if got == ".." || got == "" {
		t.Fatalf("相対 gitdir が誤解決された: %q", got)
	}
}

func TestProjectKey_MalformedGitFileFallsBackToDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitdir line\n"), 0o644); err != nil {
		t.Fatalf("prep: %v", err)
	}
	if got, want := memory.ProjectKey(dir), memory.ProjectKey(dir); got != want || got == "" {
		t.Fatalf("got %q", got)
	}
	resolved, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(memory.ProjectKey(dir), filepath.Base(resolved)) {
		t.Fatalf("不正な .git ファイルではそのディレクトリをキーにするべき: %q", memory.ProjectKey(dir))
	}
}

func TestStore_ConcurrentAppendKeepsSizeLimit(t *testing.T) {
	s, _ := newStore(t)
	chunk := strings.Repeat("a", 1<<18) // 256 KiB
	var wg sync.WaitGroup
	var okCount atomic.Int32
	for range 8 {
		wg.Go(func() {
			if err := s.Write("big.md", chunk, true); err == nil {
				okCount.Add(1)
			}
		})
	}
	wg.Wait()
	// 1 MiB 上限のため成功は最大 4 回。並行しても上限を超えて書かれない
	if okCount.Load() > 4 {
		t.Fatalf("並行 append で上限を超えた: %d 回成功", okCount.Load())
	}
	fi, err := os.Stat(filepath.Join(s.Dir(), "big.md"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() > 1<<20 {
		t.Fatalf("ファイルサイズが上限超過: %d", fi.Size())
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
