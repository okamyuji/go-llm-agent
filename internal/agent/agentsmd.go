package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// agentsMDFileName 探索対象のファイル名。大文字小文字を含め固定とする
const agentsMDFileName = "AGENTS.md"

// LoadAgentsMD startDir から祖先ディレクトリを / へ向けて 1 段ずつ辿り、
// 最初に見つかった AGENTS.md の内容と絶対パスを返す。
// agent chat の起動経路は internal/instructions.Discover (階層連結・import 展開・
// 合計上限つき) へ移行済みで、本関数は単一ファイル探索の互換 API として残す。
// 探索規則を変える場合は instructions 側を正とする。
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
		found, content, readErr := readAgentsMDCandidate(candidate, maxBytes)
		switch {
		case readErr == nil && found:
			return content, candidate, nil
		case readErr == nil:
			// このディレクトリには無い、またはシンボリックリンク/非通常ファイルで
			// あるため信頼しない (info-disclosure-symlink 対策)。1 段上へ
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

// readAgentsMDCandidate candidate を検査し、信頼できる通常ファイルとして
// 読める場合だけ found=true で内容を返す。
//
// セキュリティレビュー指摘への対応:
//   - info-disclosure-symlink: candidate がシンボリックリンクの場合、リンク先を
//     検証なしに読むとリポジトリ外の任意ファイル (例: /etc 配下) の内容を
//     プロンプトへ流し込める。os.Lstat でシンボリックリンクを検出し、読まずに
//     found=false を返す (探索は上位ディレクトリへ継続する。エラーにはしない —
//     プロジェクトが意図せずシンボリックリンクを置いているだけの場合に
//     探索全体を止めるのは過剰なため)。
//   - resource-cap-defeat: os.ReadFile で全文を読んでから truncate する実装では
//     巨大ファイルに対して max_bytes が効かず、ディスク上の全内容をメモリに
//     読み込んでしまう。os.Open + io.LimitReader(f, maxBytes+1) で
//     読み取りバイト数そのものを上限に抑える。maxBytes<=0 (切り詰め無効、
//     関数単体の契約としてのみ許容。config 経由では届かない) のときは
//     従来どおり全文を読む。
//
// candidate が存在しない場合は found=false, err=nil を返す (呼び出し元が
// 上位ディレクトリへの継続に使う)。ディレクトリの場合はエラーを返す
// (fs 境界の検証失敗を明示する既存方針に合わせる)。
func readAgentsMDCandidate(candidate string, maxBytes int) (found bool, content string, err error) {
	fi, statErr := os.Lstat(candidate)
	switch {
	case statErr == nil && fi.Mode()&os.ModeSymlink != 0:
		return false, "", nil
	case statErr == nil && fi.IsDir():
		return false, "", fmt.Errorf("%s is a directory", candidate)
	case statErr == nil && !fi.Mode().IsRegular():
		// デバイスファイル・FIFO・ソケット等。通常ファイルではないため読まない
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

// readCapped path を開き、maxBytes > 0 のときは最大 maxBytes+1 バイトだけを
// 読む (truncateAtRuneBoundary が境界判定できるよう 1 バイト多く読む)。
// maxBytes <= 0 のときは全文を読む
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
