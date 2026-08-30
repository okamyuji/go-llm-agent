// Package instructions 階層指示ファイル (AGENTS.md) の探索・連結・import 展開を提供する
// 18 番設計書のとおり、グローバル → プロジェクトルート → cwd の順に集め、
// cwd に近いファイルほど後方 (実質優先) に置く
package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/fsx"
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
	c := &collector{opt: opt}
	if globalDir != "" {
		cont, err := c.add(filepath.Join(globalDir, instructionsFileName), "global", []string{globalDir})
		if err != nil || !cont {
			return c.sources, err
		}
	}
	chain, err := projectChain(cwd, allowPaths)
	if err != nil {
		return nil, err
	}
	// allowPaths が空のとき chain は cwd の 1 件であり、import の探索ルートも cwd とする
	roots := allowPaths
	if len(roots) == 0 {
		roots = []string{chain[0]}
	}
	for _, dir := range chain {
		cont, err := c.add(filepath.Join(dir, instructionsFileName), "project", roots)
		if err != nil || !cont {
			return c.sources, err
		}
	}
	return c.sources, nil
}

// collector 連結対象を収集し、合計バイト上限を追跡する
type collector struct {
	opt     Options
	total   int
	sources []Source
}

// add path を読み、import を展開して sources へ追加する。戻り値 cont=false は
// 合計上限へ達したため以降のファイルを追加しないことを示す。
// 読めない・空のファイルはスキップして cont=true を返す
func (c *collector) add(path, scope string, roots []string) (cont bool, err error) {
	found, content, err := readCandidate(path, c.opt.FileMaxBytes)
	if err != nil {
		return false, fmt.Errorf("instructions: read %s: %w", path, err)
	}
	if !found || content == "" {
		return true, nil
	}
	visited := map[string]bool{filepath.Clean(path): true}
	content = expandImports(content, filepath.Dir(path), roots, c.opt, 0, visited)
	if c.opt.TotalMaxBytes > 0 && c.total+len(content) > c.opt.TotalMaxBytes {
		return false, nil
	}
	c.total += len(content)
	c.sources = append(c.sources, Source{Path: path, Content: content, Scope: scope})
	return true, nil
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
	for dir := abs; withinAllowPaths(dir, allowPaths); {
		chain = append(chain, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// 浅い祖先が先頭になるよう反転する
	slices.Reverse(chain)
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
// ディレクトリと権限エラーはエラーとして返す (fs 境界の検証失敗を明示する方針)。
// Lstat と open の間の差し替え (TOCTOU) は fsx.ReadCapped 側の O_NOFOLLOW が塞ぐ
func readCandidate(candidate string, maxBytes int) (found bool, content string, err error) {
	fi, statErr := os.Lstat(candidate)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", statErr
	}
	if fi.IsDir() {
		return false, "", fmt.Errorf("%s is a directory", candidate)
	}
	// シンボリックリンク・デバイスファイル・FIFO 等は通常ファイルではないため読まない
	if !fi.Mode().IsRegular() {
		return false, "", nil
	}
	c, rerr := fsx.ReadCapped(candidate, maxBytes)
	if rerr != nil {
		return false, "", rerr
	}
	return true, c, nil
}
