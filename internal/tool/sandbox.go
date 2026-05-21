package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// sensitivePatterns deny 対象のセンシティブな名前/接頭辞のハードコードリスト
// プロンプトインジェクションや破壊操作の典型的標的を遮断する。
// パッケージ外からの上書きを防ぐため非公開とし、参照は SensitivePatterns() で行う。
var sensitivePatterns = []string{
	".git",
	".env",
	".env.local",
	".ssh",
	".aws",
	".gnupg",
	".npmrc",
	".netrc",
	".pypirc",
	"id_rsa",
	"id_dsa",
	"id_ecdsa",
	"id_ed25519",
}

// SensitivePatterns 強制 deny パターンの読み取り専用コピーを返す
func SensitivePatterns() []string {
	out := make([]string, len(sensitivePatterns))
	copy(out, sensitivePatterns)
	return out
}

// Sandbox 許可ルートと拒否パターンの管理
type Sandbox struct {
	allowedRoots []string
	denyPatterns []string
}

// NewSandbox 許可ルート群から Sandbox を生成する。deny パターンは未指定とする
func NewSandbox(roots []string) *Sandbox {
	return NewSandboxWithDeny(roots, nil)
}

// NewSandboxWithDeny 許可ルートと追加 deny パターンから Sandbox を生成する
func NewSandboxWithDeny(roots, deny []string) *Sandbox {
	clean := make([]string, 0, len(roots))
	for _, r := range roots {
		r = expandTilde(r)
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		clean = append(clean, abs)
	}
	// sensitivePatterns は常に強制 deny に組み込む（設定で外せない）
	denyAll := append([]string{}, sensitivePatterns...)
	denyAll = append(denyAll, deny...)
	return &Sandbox{allowedRoots: clean, denyPatterns: denyAll}
}

// CheckPath path が許可ルート配下かつ deny パターンに該当しないかを確認する
func (s *Sandbox) CheckPath(path string) error {
	if path == "" {
		return fmt.Errorf("sandbox: パスが空です")
	}
	// 正規化前の入力に .. セグメントがある場合は早期拒否
	// filepath.Clean が .. を消す前に検出することで意図を明確化する
	expanded := expandTilde(path)
	if hasDotDotSegment(expanded) {
		return fmt.Errorf("sandbox: パスに上位ディレクトリ参照が含まれています %q", path)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return fmt.Errorf("sandbox: 絶対パス変換失敗: %w", err)
	}
	clean := filepath.Clean(abs)
	// Clean 後に .. が残るケース（許可ルート外への遡上）も二段で拒否
	if hasDotDotSegment(clean) {
		return fmt.Errorf("sandbox: パスに上位ディレクトリ参照が含まれています %q", path)
	}
	// canonical 確定（存在しないパスは祖先まで遡って解決）
	canonical := canonicalize(clean)
	// allow ルート判定
	allowed := false
	for _, r := range s.allowedRoots {
		if canonical == r || strings.HasPrefix(canonical, r+string(filepath.Separator)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("sandbox: パス %q は許可ルート外", canonical)
	}
	// deny 判定（許可ルート相対のサブパスのいずれかのセグメントが deny にマッチする場合拒否）
	for _, r := range s.allowedRoots {
		if rel, ok := relIfDescendant(r, canonical); ok {
			if matchesDenySegments(rel, s.denyPatterns) {
				return fmt.Errorf("sandbox: パス %q はセンシティブなパターンに一致 (%s)", canonical, denyMatch(rel, s.denyPatterns))
			}
			break
		}
	}
	return nil
}

// canonicalize 存在しないパスでも EvalSymlinks できるように、存在する祖先まで遡る
func canonicalize(p string) string {
	cur := p
	tail := ""
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if tail == "" {
				return resolved
			}
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

func hasDotDotSegment(p string) bool {
	return slices.Contains(strings.Split(p, string(filepath.Separator)), "..")
}

func relIfDescendant(root, target string) (string, bool) {
	if target == root {
		return ".", true
	}
	prefix := root + string(filepath.Separator)
	if rest, ok := strings.CutPrefix(target, prefix); ok {
		return rest, true
	}
	return "", false
}

func matchesDenySegments(rel string, patterns []string) bool {
	return denyMatch(rel, patterns) != ""
}

func denyMatch(rel string, patterns []string) string {
	for seg := range strings.SplitSeq(filepath.ToSlash(rel), "/") {
		if seg == "" {
			continue
		}
		for _, pat := range patterns {
			if matchSensitive(seg, pat) {
				return pat
			}
		}
	}
	return ""
}

// matchSensitive 単一セグメントとパターンの一致判定
// パターンに glob 文字があれば filepath.Match、なければ完全一致または接頭辞一致
func matchSensitive(seg, pat string) bool {
	if pat == "" {
		return false
	}
	if strings.ContainsAny(pat, "*?[") {
		ok, _ := filepath.Match(pat, seg)
		return ok
	}
	if seg == pat {
		return true
	}
	// id_rsa* のような典型接頭辞を考慮（id_rsa, id_rsa.pub 等）
	if strings.HasPrefix(seg, pat+".") {
		return true
	}
	return false
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
