package instructions_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/instructions"
)

// discoverOne root 直下の AGENTS.md 1 件を Discover して返すヘルパ
func discoverOne(t *testing.T, root string, opt instructions.Options) instructions.Source {
	t.Helper()
	srcs, err := instructions.Discover("", root, []string{root}, opt)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 1 {
		t.Fatalf("len=%d, want 1 (%+v)", len(srcs), srcs)
	}
	return srcs[0]
}

func TestImports_LineHeadOnlyExpanded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "extra.md"), "imported body")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "head\n@extra.md\ninline @extra.md stays\n")

	src := discoverOne(t, root, defaultOpt())
	if !strings.Contains(src.Content, "imported body") {
		t.Fatalf("import 未展開: %q", src.Content)
	}
	if !strings.Contains(src.Content, "inline @extra.md stays") {
		t.Fatalf("行中の @ が展開された: %q", src.Content)
	}
}

func TestImports_RelativeToDeclaringFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sub", "inner.md"), "inner")
	writeFile(t, filepath.Join(root, "sub", "outer.md"), "@inner.md\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@sub/outer.md\n")

	src := discoverOne(t, root, defaultOpt())
	if !strings.Contains(src.Content, "inner") {
		t.Fatalf("記述ファイル基準の相対解決が効いていない: %q", src.Content)
	}
}

func TestImports_DepthLimit(t *testing.T) {
	root := t.TempDir()
	// AGENTS.md -> d1 -> d2 -> d3 -> d4 -> d5 と辿る。深さ 4 で d5 は展開されない
	writeFile(t, filepath.Join(root, "d5.md"), "LEVEL5")
	writeFile(t, filepath.Join(root, "d4.md"), "LEVEL4\n@d5.md\n")
	writeFile(t, filepath.Join(root, "d3.md"), "LEVEL3\n@d4.md\n")
	writeFile(t, filepath.Join(root, "d2.md"), "LEVEL2\n@d3.md\n")
	writeFile(t, filepath.Join(root, "d1.md"), "LEVEL1\n@d2.md\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@d1.md\n")

	src := discoverOne(t, root, defaultOpt())
	if !strings.Contains(src.Content, "LEVEL4") {
		t.Fatalf("深さ 4 まで展開されるべき: %q", src.Content)
	}
	if strings.Contains(src.Content, "LEVEL5") {
		t.Fatalf("深さ 5 が展開された: %q", src.Content)
	}
}

func TestImports_CycleStops(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "A\n@b.md\n")
	writeFile(t, filepath.Join(root, "b.md"), "B\n@a.md\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@a.md\n")

	src := discoverOne(t, root, defaultOpt())
	if strings.Count(src.Content, "A") != 1 || strings.Count(src.Content, "B") != 1 {
		t.Fatalf("循環が止まっていない: %q", src.Content)
	}
}

func TestImports_DiamondExpandsBothPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "common.md"), "COMMON")
	writeFile(t, filepath.Join(root, "sub", "other.md"), "@../common.md\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@common.md\n@sub/other.md\n")

	src := discoverOne(t, root, defaultOpt())
	if strings.Count(src.Content, "COMMON") != 2 {
		t.Fatalf("ダイヤモンド構成で共通ファイルが 2 回展開されない: %q", src.Content)
	}
	if strings.Contains(src.Content, "@../common.md") {
		t.Fatalf("未展開の import 行が残った: %q", src.Content)
	}
}

func TestImports_CodeFenceSkipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "extra.md"), "SHOULD_NOT_APPEAR")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "```\n@extra.md\n```\n")

	src := discoverOne(t, root, defaultOpt())
	if strings.Contains(src.Content, "SHOULD_NOT_APPEAR") {
		t.Fatalf("フェンス内の import が展開された: %q", src.Content)
	}
}

func TestImports_OutsideRootRejected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	writeFile(t, filepath.Join(base, "secret.md"), "SECRET")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@../secret.md\n")

	src := discoverOne(t, root, defaultOpt())
	if strings.Contains(src.Content, "SECRET") {
		t.Fatalf("探索ルート外が読まれた: %q", src.Content)
	}
}

func TestImports_MissingFileKeepsLine(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@missing.md\ntail\n")

	src := discoverOne(t, root, defaultOpt())
	if !strings.Contains(src.Content, "@missing.md") {
		t.Fatalf("存在しない import の行が消えた: %q", src.Content)
	}
	if !strings.Contains(src.Content, "tail") {
		t.Fatalf("後続行が消えた: %q", src.Content)
	}
}

func TestImports_AbsolutePathRejected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	secret := filepath.Join(base, "secret.md")
	writeFile(t, secret, "SECRET")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "@"+secret+"\n")

	src := discoverOne(t, root, defaultOpt())
	if strings.Contains(src.Content, "SECRET") {
		t.Fatalf("絶対パス import が読まれた: %q", src.Content)
	}
}

func TestImports_GlobalScopeUsesGlobalRoot(t *testing.T) {
	global := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(global, "extra.md"), "global extra")
	writeFile(t, filepath.Join(global, "AGENTS.md"), "@extra.md\n")

	srcs, err := instructions.Discover(global, cwd, []string{cwd}, defaultOpt())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(srcs) != 1 || !strings.Contains(srcs[0].Content, "global extra") {
		t.Fatalf("グローバル側 import が展開されていない: %+v", srcs)
	}
}
