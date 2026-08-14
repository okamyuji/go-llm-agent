package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// agentsMDFileName 探索対象のファイル名。大文字小文字を含め固定とする
const agentsMDFileName = "AGENTS.md"

// LoadAgentsMD startDir から祖先ディレクトリを / へ向けて 1 段ずつ辿り、
// 最初に見つかった AGENTS.md の内容と絶対パスを返す。
// 探索は allowPaths のいずれかの配下にあるディレクトリだけを対象とし、
// どの allowPaths 配下でもないディレクトリに到達した時点で打ち切る
// (07-agents-md.md §2.1 の 2 つめ)。allowPaths が空の場合は startDir 自身のみを見る。
// 見つからない場合は content="" path="" err=nil を返す（エラーではない）。
// maxBytes を超える内容は UTF-8 の rune 境界を壊さない範囲で先頭のみへ
// 切り詰める。maxBytes <= 0 のときは切り詰めを行わない。
func LoadAgentsMD(startDir string, maxBytes int, allowPaths []string) (content string, path string, err error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", "", fmt.Errorf("agentsmd: resolve start dir %q: %w", startDir, err)
	}
	for {
		if !withinAllowPaths(dir, allowPaths) {
			// サンドボックス対象外のディレクトリへ出た。これ以上は遡らない
			return "", "", nil
		}
		candidate := filepath.Join(dir, agentsMDFileName)
		b, readErr := os.ReadFile(candidate)
		switch {
		case readErr == nil:
			return truncateAtRuneBoundary(string(b), maxBytes), candidate, nil
		case os.IsNotExist(readErr):
			// このディレクトリには無い。1 段上へ
		default:
			// 権限エラー等はサイレントに握りつぶさず起動時エラーとして報告する
			// (fs 境界での検証は失敗を明示するという既存の engineering 方針に合わせる)
			return "", "", fmt.Errorf("agentsmd: read %s: %w", candidate, readErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// ルートに到達し、どの祖先にも見つからなかった
			return "", "", nil
		}
		dir = parent
	}
}

// withinAllowPaths dir が allowPaths のいずれかの配下 (または一致) かを返す。
// allowPaths が空の場合は false を返し、呼び出し元は startDir 自身のみを
// 探索対象とする (LoadAgentsMD の初回反復のみ true として扱う特例は設けず、
// 呼び出し元が allowPaths 空のとき startDir を要素 1 件として渡す)
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

// truncateAtRuneBoundary s が maxBytes バイトを超える場合、maxBytes バイト目の
// 直前にある rune 境界までを返す。maxBytes <= 0 または超過しない場合は
// そのまま返す。
// 末尾の不完全なシーケンスだけを削る実装にしている。utf8.ValidString で
// 文字列全体の妥当性を見ながら 1 バイトずつ削る方式にすると、内容の中央
// (例: 先頭から 100 バイト目) に不正バイトがある場合に末尾から 100 バイト目の
// 直前まで削られ、内容の大半が無警告で消える。
func truncateAtRuneBoundary(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	b := s[:maxBytes]
	// 末尾が不完全なシーケンスなら、その開始バイトの手前まで戻す
	for len(b) > 0 {
		r, size := utf8.DecodeLastRuneInString(b)
		if r != utf8.RuneError || size > 1 {
			break // 末尾の rune は完結している (size>1 の RuneError は不正バイトだが完結扱い)
		}
		b = b[:len(b)-1]
	}
	return b
}
