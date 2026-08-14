package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// mkTree root/proj/sub の 3 段ディレクトリを作り sub のパスを返す
func mkTree(t *testing.T) (root, proj, sub string) {
	t.Helper()
	root = t.TempDir()
	proj = filepath.Join(root, "proj")
	sub = filepath.Join(proj, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll err=%v", err)
	}
	return root, proj, sub
}

func writeAgentsMD(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, agentsMDFileName), []byte(content), 0o600); err != nil {
		t.Fatalf("write AGENTS.md err=%v", err)
	}
}

func TestLoadAgentsMD_WalksUpTwoLevels(t *testing.T) {
	root, proj, sub := mkTree(t)
	writeAgentsMD(t, proj, "proj instructions")
	content, path, err := LoadAgentsMD(sub, 0, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "proj instructions" {
		t.Fatalf("content=%q", content)
	}
	if path != filepath.Join(proj, agentsMDFileName) {
		t.Fatalf("path=%q", path)
	}
}

func TestLoadAgentsMD_NearestAncestorWins(t *testing.T) {
	root, proj, sub := mkTree(t)
	writeAgentsMD(t, proj, "proj instructions")
	writeAgentsMD(t, sub, "sub instructions")
	content, path, err := LoadAgentsMD(sub, 0, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "sub instructions" {
		t.Fatalf("content=%q, want nearest (sub)", content)
	}
	if path != filepath.Join(sub, agentsMDFileName) {
		t.Fatalf("path=%q", path)
	}
}

func TestLoadAgentsMD_NoneFoundReturnsEmptyNoError(t *testing.T) {
	root, _, sub := mkTree(t)
	content, path, err := LoadAgentsMD(sub, 0, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "" || path != "" {
		t.Fatalf("content=%q path=%q, want both empty", content, path)
	}
}

func TestLoadAgentsMD_StartDirItselfHasFile(t *testing.T) {
	root, _, sub := mkTree(t)
	writeAgentsMD(t, sub, "sub instructions")
	content, path, err := LoadAgentsMD(sub, 0, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "sub instructions" || path != filepath.Join(sub, agentsMDFileName) {
		t.Fatalf("content=%q path=%q", content, path)
	}
}

func TestLoadAgentsMD_SmallerThanMaxBytesNoTruncation(t *testing.T) {
	root, _, sub := mkTree(t)
	writeAgentsMD(t, sub, "short")
	content, _, err := LoadAgentsMD(sub, 1000, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "short" {
		t.Fatalf("content=%q", content)
	}
}

func TestLoadAgentsMD_ExactMaxBytesNoTruncation(t *testing.T) {
	root, _, sub := mkTree(t)
	writeAgentsMD(t, sub, "12345")
	content, _, err := LoadAgentsMD(sub, 5, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "12345" {
		t.Fatalf("content=%q", content)
	}
}

func TestLoadAgentsMD_MaxBytesMidMultibyteCharTruncatesBefore(t *testing.T) {
	root, _, sub := mkTree(t)
	// "あ" は3バイト。maxBytes=4 は 1 バイト目の途中を指す
	writeAgentsMD(t, sub, "あああ")
	content, _, err := LoadAgentsMD(sub, 4, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !utf8.ValidString(content) {
		t.Fatalf("content is not valid UTF-8: %q", content)
	}
	if content != "あ" {
		t.Fatalf("content=%q, want 一文字目のみ", content)
	}
}

func TestLoadAgentsMD_InvalidByteMidContentBeyondMaxBytesKeepsSuffix(t *testing.T) {
	root, _, sub := mkTree(t)
	prefix := strings.Repeat("x", 100)
	suffix := "known-marker-after-invalid-byte-" + strings.Repeat("y", 200)
	content := prefix + string([]byte{0xFF}) + suffix
	if err := os.WriteFile(filepath.Join(sub, agentsMDFileName), []byte(content), 0o600); err != nil {
		t.Fatalf("write err=%v", err)
	}
	maxBytes := 150
	got, _, err := LoadAgentsMD(sub, maxBytes, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(got) < maxBytes-3 || len(got) > maxBytes {
		t.Fatalf("len(got)=%d, want close to maxBytes=%d", len(got), maxBytes)
	}
	if !strings.Contains(got, "known-marker-after-invalid-byte") {
		t.Fatalf("got=%q, want it to retain content past the invalid byte at 100", got)
	}
}

func TestLoadAgentsMD_InvalidByteMidContentWithinMaxBytesUntouched(t *testing.T) {
	root, _, sub := mkTree(t)
	content := "prefix" + string([]byte{0xFF}) + "suffix"
	if err := os.WriteFile(filepath.Join(sub, agentsMDFileName), []byte(content), 0o600); err != nil {
		t.Fatalf("write err=%v", err)
	}
	got, _, err := LoadAgentsMD(sub, 10000, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != content {
		t.Fatalf("got=%q, want unchanged content", got)
	}
}

func TestLoadAgentsMD_MaxBytesNonPositiveNoTruncation(t *testing.T) {
	root, _, sub := mkTree(t)
	big := strings.Repeat("z", 1000)
	writeAgentsMD(t, sub, big)
	content, _, err := LoadAgentsMD(sub, 0, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != big {
		t.Fatalf("len(content)=%d, want %d (maxBytes<=0 disables truncation)", len(content), len(big))
	}
}

func TestLoadAgentsMD_AllowPathsRepoRootFindsRootFile(t *testing.T) {
	root, _, sub := mkTree(t)
	writeAgentsMD(t, root, "root instructions")
	content, path, err := LoadAgentsMD(sub, 0, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "root instructions" || path != filepath.Join(root, agentsMDFileName) {
		t.Fatalf("content=%q path=%q", content, path)
	}
}

func TestLoadAgentsMD_AllowPathsExcludesAncestorOutsideRange(t *testing.T) {
	root, proj, sub := mkTree(t)
	writeAgentsMD(t, root, "root instructions (out of range)")
	content, path, err := LoadAgentsMD(sub, 0, []string{proj})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "" || path != "" {
		t.Fatalf("content=%q path=%q, want both empty (root is outside allowPaths)", content, path)
	}
}

func TestLoadAgentsMD_AllowPathsPicksNearestWithinRange(t *testing.T) {
	root, proj, _ := mkTree(t)
	writeAgentsMD(t, proj, "proj instructions")
	writeAgentsMD(t, root, "root instructions")
	content, path, err := LoadAgentsMD(proj, 0, []string{proj})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "proj instructions" || path != filepath.Join(proj, agentsMDFileName) {
		t.Fatalf("content=%q path=%q", content, path)
	}
}

func TestLoadAgentsMD_EmptyAllowPathsFindsNothing(t *testing.T) {
	_, _, sub := mkTree(t)
	writeAgentsMD(t, sub, "sub instructions")
	content, path, err := LoadAgentsMD(sub, 0, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "" || path != "" {
		t.Fatalf("content=%q path=%q, want both empty (empty allowPaths excludes startDir itself)", content, path)
	}
}

func TestLoadAgentsMD_DirectoryNamedAGENTSMDReturnsError(t *testing.T) {
	root, _, sub := mkTree(t)
	if err := os.Mkdir(filepath.Join(sub, agentsMDFileName), 0o755); err != nil {
		t.Fatalf("mkdir err=%v", err)
	}
	_, _, err := LoadAgentsMD(sub, 0, []string{root})
	if err == nil {
		t.Fatal("want error when AGENTS.md is a directory")
	}
}

func TestLoadAgentsMD_UnreadableFileReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root では chmod 0o000 が効かない")
	}
	root, _, sub := mkTree(t)
	writeAgentsMD(t, sub, "secret")
	p := filepath.Join(sub, agentsMDFileName)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatalf("chmod err=%v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	_, _, err := LoadAgentsMD(sub, 0, []string{root})
	if err == nil {
		t.Fatal("want error for unreadable AGENTS.md")
	}
}

func TestLoadAgentsMD_RelativeStartDirIsResolved(t *testing.T) {
	root, _, sub := mkTree(t)
	writeAgentsMD(t, sub, "sub instructions")
	rel, err := filepath.Rel(root, sub)
	if err != nil {
		t.Fatalf("Rel err=%v", err)
	}
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd err=%v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir err=%v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	// os.Chdir + os.Getwd (filepath.Abs が内部で使う) はシンボリックリンクを
	// 物理パスへ解決した cwd を返す (darwin の t.TempDir() は /var/folders ->
	// /private/var/folders のシンボリックリンク配下)。allowPaths 側も同じ
	// 物理パスで渡さないと withinAllowPaths が一致しないため解決してから渡す
	// (実運用では os.Getwd() 由来の cwd と cfg.Tools.FS.AllowPaths の両方が
	// 同じ解決規則で揃っている前提)
	resolvedRoot, everr := filepath.EvalSymlinks(root)
	if everr != nil {
		t.Fatalf("EvalSymlinks err=%v", everr)
	}

	content, path, lerr := LoadAgentsMD(rel, 0, []string{resolvedRoot})
	if lerr != nil {
		t.Fatalf("err=%v", lerr)
	}
	wantPath, everr2 := filepath.EvalSymlinks(filepath.Join(sub, agentsMDFileName))
	if everr2 != nil {
		t.Fatalf("EvalSymlinks err=%v", everr2)
	}
	if content != "sub instructions" || path != wantPath {
		t.Fatalf("content=%q path=%q want path=%q", content, path, wantPath)
	}
}

// TestLoadAgentsMD_SymlinkToOutsideFileIsNotFollowed info-disclosure-symlink 対策の確認。
// sub/AGENTS.md がリポジトリ外 (/etc 配下) を指すシンボリックリンクの場合、
// リンク先を読まず、探索は上位ディレクトリへ継続して proj/AGENTS.md を返す
func TestLoadAgentsMD_SymlinkToOutsideFileIsNotFollowed(t *testing.T) {
	root, proj, sub := mkTree(t)
	writeAgentsMD(t, proj, "proj instructions")

	target := "/etc/hosts"
	if _, statErr := os.Stat(target); statErr != nil {
		t.Skip("/etc/hosts が無い環境のため skip")
	}
	link := filepath.Join(sub, agentsMDFileName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink err=%v", err)
	}

	content, path, err := LoadAgentsMD(sub, 0, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "proj instructions" || path != filepath.Join(proj, agentsMDFileName) {
		t.Fatalf("content=%q path=%q, want proj/AGENTS.md (symlink を辿らず上位へ継続すること)", content, path)
	}
	if strings.Contains(content, "127.0.0.1") || strings.Contains(content, "localhost") {
		t.Fatalf("content にリンク先 (/etc/hosts) の内容が含まれてはいけない: %q", content)
	}
}

// TestLoadAgentsMD_SymlinkWithNoOtherCandidateFindsNothing シンボリックリンクしか
// 存在しない場合、探索全体が「見つからない」で終わる (エラーにしない)
func TestLoadAgentsMD_SymlinkWithNoOtherCandidateFindsNothing(t *testing.T) {
	root, _, sub := mkTree(t)
	target := "/etc/hosts"
	if _, statErr := os.Stat(target); statErr != nil {
		t.Skip("/etc/hosts が無い環境のため skip")
	}
	if err := os.Symlink(target, filepath.Join(sub, agentsMDFileName)); err != nil {
		t.Fatalf("Symlink err=%v", err)
	}
	content, path, err := LoadAgentsMD(sub, 0, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if content != "" || path != "" {
		t.Fatalf("content=%q path=%q, want both empty", content, path)
	}
}

// TestLoadAgentsMD_HugeFileReadIsCappedAtMaxBytes resource-cap-defeat 対策の確認。
// max_bytes を大きく超えるファイルに対して、返る内容のバイト数が max_bytes 近傍
// (rune 境界調整分の数バイト以内) に収まること = 実装が全文を読んでから truncate
// するのではなく、読み取りそのものを max_bytes+1 バイトに制限していることの
// 間接的な確認 (io.LimitReader の使用箇所を通ること)
func TestLoadAgentsMD_HugeFileReadIsCappedAtMaxBytes(t *testing.T) {
	root, _, sub := mkTree(t)
	const hugeSize = 5 * 1024 * 1024 // 5MiB。max_bytes (100) を大きく超える
	big := strings.Repeat("a", hugeSize)
	writeAgentsMD(t, sub, big)

	const maxBytes = 100
	content, _, err := LoadAgentsMD(sub, maxBytes, []string{root})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(content) > maxBytes {
		t.Fatalf("len(content)=%d, want <= maxBytes=%d (読み取りが上限で抑えられていない)", len(content), maxBytes)
	}
	if len(content) < maxBytes-3 {
		t.Fatalf("len(content)=%d, want close to maxBytes=%d (ASCII のみなので rune 境界調整は発生しないはず)", len(content), maxBytes)
	}
}

// TestReadCapped_ReadsAtMostMaxBytesPlusOne readCapped がファイルサイズに関わらず
// maxBytes+1 バイトしか読まないことを直接確認する
func TestReadCapped_ReadsAtMostMaxBytesPlusOne(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.txt")
	const size = 1024 * 1024
	if err := os.WriteFile(p, []byte(strings.Repeat("b", size)), 0o600); err != nil {
		t.Fatalf("write err=%v", err)
	}
	const maxBytes = 10
	content, err := readCapped(p, maxBytes)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(content) != maxBytes {
		t.Fatalf("len(content)=%d, want exactly %d (ASCII, no rune boundary trimming needed)", len(content), maxBytes)
	}
}

func TestReadAgentsMDCandidate_SymlinkReturnsNotFound(t *testing.T) {
	dir := t.TempDir()
	target := "/etc/hosts"
	if _, statErr := os.Stat(target); statErr != nil {
		t.Skip("/etc/hosts が無い環境のため skip")
	}
	link := filepath.Join(dir, agentsMDFileName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink err=%v", err)
	}
	found, content, err := readAgentsMDCandidate(link, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if found || content != "" {
		t.Fatalf("found=%v content=%q, want not-found for symlink candidate", found, content)
	}
}

func TestReadAgentsMDCandidate_MissingFileReturnsNotFound(t *testing.T) {
	found, content, err := readAgentsMDCandidate(filepath.Join(t.TempDir(), "AGENTS.md"), 0)
	if err != nil || found || content != "" {
		t.Fatalf("found=%v content=%q err=%v, want not-found no-error", found, content, err)
	}
}

func TestWithinAllowPaths_EmptyAllowPathsIsFalse(t *testing.T) {
	if withinAllowPaths("/anything", nil) {
		t.Fatal("空の allowPaths は常に false")
	}
}

func TestWithinAllowPaths_ExactMatchIsTrue(t *testing.T) {
	if !withinAllowPaths("/a/b", []string{"/a/b"}) {
		t.Fatal("完全一致は true")
	}
}

func TestTruncateAtRuneBoundary_NegativeMaxBytesNoTruncation(t *testing.T) {
	if got := truncateAtRuneBoundary("hello", -1); got != "hello" {
		t.Fatalf("got=%q", got)
	}
}
