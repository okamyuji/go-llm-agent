package instructions

import (
	"path/filepath"
	"strings"
)

// importPrefix 行頭でこの文字から始まる行を import として解釈する
const importPrefix = "@"

// expandImports content 中の行頭 `@相対パス` 行を展開して返す。
// 解決は記述ファイルのディレクトリ (baseDir) 基準で、roots のいずれかの配下に
// あるファイルだけを読む。コードフェンス内は解釈しない。depth が maxDepth に
// 達した行、絶対パス・roots 外・存在しない・読めないファイルの行は原文のまま残す。
// 循環は visited (絶対パス集合) で遮断する
func expandImports(content, baseDir string, roots []string, opt Options, depth int, visited map[string]bool) string {
	lines := strings.Split(content, "\n")
	var out []string
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		rel, ok := strings.CutPrefix(line, importPrefix)
		if inFence || !ok || depth >= opt.ImportDepth {
			out = append(out, line)
			continue
		}
		rel = strings.TrimSpace(rel)
		expanded, ok := readImport(rel, baseDir, roots, opt, depth, visited)
		if !ok {
			out = append(out, line)
			continue
		}
		out = append(out, expanded)
	}
	return strings.Join(out, "\n")
}

// readImport rel を baseDir 基準で解決し、roots 内の通常ファイルであれば
// 再帰展開済みの内容を返す。読めない・境界外・循環のときは ok=false
func readImport(rel string, baseDir string, roots []string, opt Options, depth int, visited map[string]bool) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", false
	}
	path := filepath.Clean(filepath.Join(baseDir, rel))
	if !withinAllowPaths(filepath.Dir(path), roots) {
		return "", false
	}
	if visited[path] {
		return "", false
	}
	found, content, err := readCandidate(path, opt.FileMaxBytes)
	if err != nil || !found {
		return "", false
	}
	visited[path] = true
	return expandImports(content, filepath.Dir(path), roots, opt, depth+1, visited), true
}
