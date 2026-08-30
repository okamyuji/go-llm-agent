// Package instructions 階層指示ファイル (AGENTS.md) の探索・連結・import 展開を提供する
// 18 番設計書のとおり、グローバル → プロジェクトルート → cwd の順に集め、
// cwd に近いファイルほど後方 (実質優先) に置く
package instructions

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// instructionsFileName 探索対象のファイル名。大文字小文字を含め固定とする
const instructionsFileName = "AGENTS.md"

// Source 連結対象1ファイル
type Source struct {
	Path    string // 絶対パス
	Content string // import展開済みの内容
	Scope   string // "global" | "project"
}

// Options Discover の探索パラメータ
type Options struct {
	FileMaxBytes  int // 1 ファイルの上限。<=0 は無制限
	TotalMaxBytes int // 連結合計の上限。<=0 は無制限
	ImportDepth   int // @import の展開深さ上限
}

// Discover グローバル → プロジェクトルート → cwd の順に AGENTS.md を集める。
// プロジェクト側は allowPaths 配下にある cwd の最も浅い祖先から cwd へ向かって
// 各ディレクトリを 1 つずつ見る。allowPaths が空のときは cwd 自身のみを見る。
// 空ファイルはスキップし、合計が TotalMaxBytes へ達した時点で以降のファイルを
// 追加しない
func Discover(globalDir, cwd string, allowPaths []string, opt Options) ([]Source, error) {
	var sources []Source
	total := 0

	appendSource := func(path, scope string) (bool, error) {
		found, content, err := readCandidate(path, opt.FileMaxBytes)
		if err != nil {
			return true, fmt.Errorf("instructions: read %s: %w", path, err)
		}
		if !found || content == "" {
			return true, nil
		}
		if opt.TotalMaxBytes > 0 && total+len(content) > opt.TotalMaxBytes {
			// 合計上限へ達した。以降のファイルは追加しない
			return false, nil
		}
		total += len(content)
		sources = append(sources, Source{Path: path, Content: content, Scope: scope})
		return true, nil
	}

	if globalDir != "" {
		cont, err := appendSource(filepath.Join(globalDir, instructionsFileName), "global")
		if err != nil {
			return nil, err
		}
		if !cont {
			return sources, nil
		}
	}

	chain, err := projectChain(cwd, allowPaths)
	if err != nil {
		return nil, err
	}
	for _, dir := range chain {
		cont, err := appendSource(filepath.Join(dir, instructionsFileName), "project")
		if err != nil {
			return nil, err
		}
		if !cont {
			break
		}
	}
	return sources, nil
}

// projectChain allowPaths 配下にある cwd の最も浅い祖先から cwd までの
// ディレクトリ列を返す。allowPaths が空のときは cwd のみを返す
func projectChain(cwd string, allowPaths []string) ([]string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("instructions: resolve cwd %q: %w", cwd, err)
	}
	if len(allowPaths) == 0 {
		return []string{abs}, nil
	}
	var chain []string
	dir := abs
	for {
		if !withinAllowPaths(dir, allowPaths) {
			break
		}
		chain = append(chain, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// 浅い祖先が先頭になるよう反転する
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

// withinAllowPaths dir が allowPaths のいずれかの配下 (または一致) かを返す
func withinAllowPaths(dir string, allowPaths []string) bool {
	clean := filepath.Clean(dir)
	for _, p := range allowPaths {
		root := filepath.Clean(p)
		if clean == root {
			return true
		}
		if rel, err := filepath.Rel(root, clean); err == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// readCandidate candidate を検査し、信頼できる通常ファイルとして読める場合だけ
// found=true で内容を返す。シンボリックリンク・非通常ファイルは読まずにスキップし、
// ディレクトリと権限エラーはエラーとして返す (fs 境界の検証失敗を明示する方針)
func readCandidate(candidate string, maxBytes int) (found bool, content string, err error) {
	fi, statErr := os.Lstat(candidate)
	switch {
	case statErr == nil && fi.Mode()&os.ModeSymlink != 0:
		return false, "", nil
	case statErr == nil && fi.IsDir():
		return false, "", fmt.Errorf("%s is a directory", candidate)
	case statErr == nil && !fi.Mode().IsRegular():
		return false, "", nil
	case statErr == nil:
		c, rerr := readCapped(candidate, maxBytes)
		if rerr != nil {
			return false, "", rerr
		}
		return true, c, nil
	case os.IsNotExist(statErr):
		return false, "", nil
	default:
		return false, "", statErr
	}
}

// readCapped path を開き、maxBytes > 0 のときは最大 maxBytes+1 バイトだけを読む
// (truncateAtRuneBoundary が境界判定できるよう 1 バイト多く読む)
func readCapped(path string, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	var r io.Reader = f
	if maxBytes > 0 {
		r = io.LimitReader(f, int64(maxBytes)+1)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return truncateAtRuneBoundary(string(b), maxBytes), nil
}

// truncateAtRuneBoundary s が maxBytes バイトを超える場合、maxBytes バイト目の
// 直前にある rune 境界までを返す。末尾の不完全なシーケンスだけを削る
func truncateAtRuneBoundary(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	b := s[:maxBytes]
	for len(b) > 0 {
		r, size := utf8.DecodeLastRuneInString(b)
		if r != utf8.RuneError || size > 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}
