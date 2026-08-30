package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/okamyuji/go-llm-agent/internal/fsx"
)

// IndexFileName 自動メモリの索引ファイル名
const IndexFileName = "MEMORY.md"

// maxMemoryFileBytes 1 ファイルの上限 (18 番設計書 6.2)
const maxMemoryFileBytes = 1 << 20

// ErrMemoryFileTooLarge Write でファイルサイズが上限を超える場合に返す
var ErrMemoryFileTooLarge = fmt.Errorf("memory: file exceeds %d bytes", maxMemoryFileBytes)

// Store 自動メモリディレクトリへの読み書き。パスはディレクトリ直下の
// `<name>.md` のみ受け付け、シンボリックリンクとディレクトリ外参照を拒否する。
// 並列ツール実行から同時に呼ばれるため、mu でサイズ検査と書き込みを 1 つの
// 臨界区間にまとめる。同じディレクトリに対しては 1 つの Store を共有すること
type Store struct {
	dir string
	mu  sync.RWMutex
}

// NewStore dir を 0o700 で作成 (存在すれば維持) して Store を返す
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("memory: mkdir %s: %w", dir, err)
	}
	// 初期化時点でシンボリックリンクを解決し、以後はその物理パス配下だけを扱う
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("memory: resolve %s: %w", dir, err)
	}
	return &Store{dir: resolved}, nil
}

// Dir 解決済みのメモリディレクトリを返す
func (s *Store) Dir() string { return s.dir }

// resolve rel を検証してディレクトリ内の絶対パスを返す。
// 受け付けるのはディレクトリ直下のフラットな `<name>.md` のみ。
// シンボリックリンクの拒否は open 時 (fsx.OpenNoFollow) が担う
func (s *Store) resolve(rel string) (string, error) {
	if rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("memory: invalid path %q", rel)
	}
	if strings.ContainsAny(rel, `/\`) {
		return "", fmt.Errorf("memory: nested path %q is not allowed", rel)
	}
	if filepath.Ext(rel) != ".md" {
		return "", fmt.Errorf("memory: only .md files are allowed, got %q", rel)
	}
	return filepath.Join(s.dir, rel), nil
}

// Read rel の内容を最大 maxBytes (rune 境界) まで読んで返す。maxBytes <= 0 は全文
func (s *Store) Read(rel string, maxBytes int) (string, error) {
	path, err := s.resolve(rel)
	if err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	content, err := fsx.ReadCapped(path, maxBytes)
	if err != nil {
		return "", fmt.Errorf("memory: read %q: %w", rel, err)
	}
	return content, nil
}

// Write rel へ content を書く。appendMode が true なら追記する。
// 書き込み後のファイルサイズが上限を超える場合は ErrMemoryFileTooLarge を返す。
// 上書きは O_TRUNC で既存内容を消す前に content のサイズを検査し、拒否時に
// 既存メモリを失わない。追記は開いた fd の Stat で既存サイズを足して検査する
func (s *Store) Write(rel, content string, appendMode bool) error {
	path, err := s.resolve(rel)
	if err != nil {
		return err
	}
	if len(content) > maxMemoryFileBytes {
		return fmt.Errorf("%w (got %d)", ErrMemoryFileTooLarge, len(content))
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := fsx.OpenNoFollow(path, flags, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open %q: %w", rel, err)
	}
	if err := writeChecked(f, content, appendMode); err != nil {
		_ = f.Close()
		return fmt.Errorf("memory: write %q: %w", rel, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("memory: close %q: %w", rel, err)
	}
	return nil
}

// writeChecked 開いた fd のサイズと content の合計が上限内であることを確認して書く
func writeChecked(f *os.File, content string, appendMode bool) error {
	size := len(content)
	if appendMode {
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		size += int(fi.Size())
	}
	if size > maxMemoryFileBytes {
		return fmt.Errorf("%w (got %d)", ErrMemoryFileTooLarge, size)
	}
	_, err := f.WriteString(content)
	return err
}

// ReadIndex MEMORY.md の先頭 maxLines 行かつ maxBytes バイト (rune 境界) を返す。
// 索引が存在しない場合は空文字列を返す (エラーではない)
func (s *Store) ReadIndex(maxLines, maxBytes int) (string, error) {
	content, err := s.Read(IndexFileName, maxBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if maxLines > 0 {
		lines := strings.SplitAfterN(content, "\n", maxLines+1)
		content = strings.Join(lines[:min(len(lines), maxLines)], "")
	}
	return content, nil
}

// List ディレクトリ直下の .md 通常ファイル名をソート済みで返す
func (s *Store) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("memory: list: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && filepath.Ext(e.Name()) == ".md" {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// ProjectKey cwd の属する git リポジトリのルート絶対パス (git 外は cwd の絶対パス)
// をパス区切りを `-` へ置換した文字列として返す。worktree や submodule の
// `.git` ファイルは `gitdir:` 参照を解決して主リポジトリのルートへ寄せる
func ProjectKey(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return sanitizeKey(cwd)
	}
	if root, ok := findGitRoot(abs); ok {
		return sanitizeKey(root)
	}
	return sanitizeKey(abs)
}

// findGitRoot dir から祖先方向へ `.git` を探し、リポジトリルートを返す
func findGitRoot(dir string) (string, bool) {
	for {
		gitPath := filepath.Join(dir, ".git")
		fi, err := os.Lstat(gitPath)
		if err == nil {
			if fi.IsDir() {
				return dir, true
			}
			if fi.Mode().IsRegular() {
				if root, ok := resolveGitFile(gitPath); ok {
					return root, true
				}
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// maxGitFileBytes worktree の .git ファイルとして読む上限。通常は 1 行のため十分大きい
const maxGitFileBytes = 4096

// resolveGitFile worktree / submodule の `.git` ファイルから主リポジトリのルートを解決する。
// `gitdir: <path>` の path は絶対パスまたは `.git` ファイルのあるディレクトリ基準の
// 相対パス (submodule は通常 `../.git/modules/<name>`)。
//
// 偽造した `.git` ファイルで他プロジェクトの gitdir を指し、そのメモリを共有させる
// 攻撃を防ぐため、gitdir 側が逆参照でこのディレクトリを指していることを検証する
// (git 2.50 で観測した実レイアウト):
//   - worktree: `<gitdir>/gitdir` に元の `.git` ファイルの絶対パスが入る
//   - submodule: `<gitdir>/config` の core.worktree が gitdir 基準の相対パスで
//     submodule ディレクトリを指す
//
// どちらの逆参照も一致しない場合は解決を拒否し、呼び出し元は `.git` ファイルの
// あるディレクトリ自身をキーにする
func resolveGitFile(path string) (string, bool) {
	gitdir, ok := readGitdirPointer(path)
	if !ok {
		return "", false
	}
	if !backReferencesGitFile(gitdir, path) {
		return "", false
	}
	sep := string(filepath.Separator)
	root, _, found := strings.Cut(gitdir, sep+".git"+sep)
	if !found || root == "" {
		return "", false
	}
	return root, true
}

// readGitdirPointer `.git` ファイルの `gitdir:` 行を絶対パスへ正規化して返す
func readGitdirPointer(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxGitFileBytes))
	if err != nil {
		return "", false
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return "", false
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(path), gitdir)
	}
	return filepath.Clean(gitdir), true
}

// backReferencesGitFile gitdir が worktree の逆参照 (`<gitdir>/gitdir`) または
// submodule の core.worktree でこの `.git` ファイルのディレクトリを指すかを返す
func backReferencesGitFile(gitdir, gitFilePath string) bool {
	self := samePathKey(filepath.Dir(gitFilePath))
	if b, err := os.ReadFile(filepath.Join(gitdir, "gitdir")); err == nil {
		back := strings.TrimSpace(string(b))
		if samePathKey(filepath.Dir(back)) == self {
			return true
		}
	}
	if b, err := os.ReadFile(filepath.Join(gitdir, "config")); err == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || strings.TrimSpace(key) != "worktree" {
				continue
			}
			wt := strings.TrimSpace(val)
			if !filepath.IsAbs(wt) {
				wt = filepath.Join(gitdir, wt)
			}
			if samePathKey(wt) == self {
				return true
			}
		}
	}
	return false
}

// samePathKey シンボリックリンクを解決した比較用パスを返す。解決できなければ Clean 結果
func samePathKey(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// keyHashLen キーに付ける SHA-256 の 16 進文字数。32 文字 (128 bit) にして、
// 同じ basename のパスを作って先頭を一致させる第二原像探索を現実的でなくする
const keyHashLen = 32

// sanitizeKey 絶対パスを `<basename>-<sha256先頭32桁>` へ変換する。basename は人が
// 見て分かる名前、ハッシュは `/work/a-b` と `/work/a/b` のような区切り置換だけでは
// 衝突するパスを区別する。basename の区切り・コロンは `-` へ置換する
func sanitizeKey(path string) string {
	clean := filepath.Clean(path)
	base := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '-'
		}
		return r
	}, filepath.Base(clean))
	base = strings.Trim(base, "-")
	sum := sha256.Sum256([]byte(clean))
	digest := hex.EncodeToString(sum[:])[:keyHashLen]
	if base == "" {
		return digest
	}
	return base + "-" + digest
}
