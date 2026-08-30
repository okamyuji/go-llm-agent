package instructions_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/instructions"
)

// writeFile ディレクトリを作りつつ AGENTS.md 等を配置するテストヘルパ
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// defaultOpt テストで使う標準 Options
func defaultOpt() instructions.Options {
	return instructions.Options{FileMaxBytes: 1 << 20, TotalMaxBytes: 1 << 20, ImportDepth: 4}
}

func TestDiscover_GlobalOnly(t *testing.T) {
	global := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(global, "AGENTS.md"), "global rules")

	srcs, err := instructions.Discover(global, cwd, []string{cwd}, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 1 {
		t.Fatalf("len=%d, want 1", len(srcs))
	}
	if srcs[0].Scope != "global" || srcs[0].Content != "global rules" {
		t.Fatalf("got %+v", srcs[0])
	}
}

func TestDiscover_ProjectAncestorToCwdOrder(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "a", "b")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root")
	writeFile(t, filepath.Join(root, "a", "AGENTS.md"), "mid")
	writeFile(t, filepath.Join(root, "a", "b", "AGENTS.md"), "leaf")

	srcs, err := instructions.Discover("", cwd, []string{root}, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	got := make([]string, 0, len(srcs))
	for _, s := range srcs {
		got = append(got, s.Content)
		if s.Scope != "project" {
			t.Fatalf("scope=%q, want project", s.Scope)
		}
	}
	want := []string{"root", "mid", "leaf"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v, want %v", got, want)
	}
}

func TestDiscover_GlobalPrecedesProject(t *testing.T) {
	global := t.TempDir()
	root := t.TempDir()
	writeFile(t, filepath.Join(global, "AGENTS.md"), "global")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "project")

	srcs, err := instructions.Discover(global, root, []string{root}, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 2 || srcs[0].Scope != "global" || srcs[1].Scope != "project" {
		t.Fatalf("got %+v", srcs)
	}
}

func TestDiscover_AncestorOutsideAllowPathsExcluded(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	cwd := filepath.Join(root, "sub")
	// allowPaths 外の base に置いた AGENTS.md は拾われない
	writeFile(t, filepath.Join(base, "AGENTS.md"), "outside")
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "inside")

	srcs, err := instructions.Discover("", cwd, []string{root}, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 1 || srcs[0].Content != "inside" {
		t.Fatalf("got %+v", srcs)
	}
}

func TestDiscover_EmptyAllowPathsUsesCwdOnly(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "sub")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "parent")
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "self")

	srcs, err := instructions.Discover("", cwd, nil, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 1 || srcs[0].Content != "self" {
		t.Fatalf("got %+v", srcs)
	}
}

func TestDiscover_SymlinkSkipped(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real.md")
	writeFile(t, target, "real")
	if err := os.Symlink(target, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	srcs, err := instructions.Discover("", root, []string{root}, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 0 {
		t.Fatalf("symlink が読まれた: %+v", srcs)
	}
}

func TestDiscover_FileMaxBytesTruncatesAtRuneBoundary(t *testing.T) {
	root := t.TempDir()
	// "あ" は 3 バイト。4 バイト上限なら 1 文字だけ残る
	writeFile(t, filepath.Join(root, "AGENTS.md"), "ああ")

	opt := defaultOpt()
	opt.FileMaxBytes = 4
	srcs, err := instructions.Discover("", root, []string{root}, opt)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 1 || srcs[0].Content != "あ" {
		t.Fatalf("got %+v", srcs)
	}
}

func TestDiscover_TotalMaxBytesStopsFurtherFiles(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "sub")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "0123456789")
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "next")

	opt := defaultOpt()
	opt.TotalMaxBytes = 10
	srcs, err := instructions.Discover("", cwd, []string{root}, opt)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 1 || srcs[0].Content != "0123456789" {
		t.Fatalf("合計上限で打ち切られていない: %+v", srcs)
	}
}

func TestDiscover_EmptyGlobalDirSkipsGlobal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "project")

	srcs, err := instructions.Discover("", root, []string{root}, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 1 || srcs[0].Scope != "project" {
		t.Fatalf("got %+v", srcs)
	}
}

func TestDiscover_EmptyFileSkipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "")

	srcs, err := instructions.Discover("", root, []string{root}, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 0 {
		t.Fatalf("空ファイルが含まれた: %+v", srcs)
	}
}
