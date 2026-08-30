package memory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// IndexFileName 自動メモリの索引ファイル名
const IndexFileName = "MEMORY.md"

// maxMemoryFileBytes 1 ファイルの上限 (18 番設計書 6.2)
const maxMemoryFileBytes = 1 << 20

// ErrMemoryFileTooLarge Write でファイルサイズが上限を超える場合に返す
var ErrMemoryFileTooLarge = fmt.Errorf("memory: file exceeds %d bytes", maxMemoryFileBytes)

// Store 自動メモリディレクトリへの読み書き。パスはディレクトリ直下の
// `<name>.md` のみ受け付け、シンボリックリンクとディレクトリ外参照を拒否する
type Store struct {
	dir string
}

// NewStore dir を 0o700 で作成 (存在すれば維持) して Store を返す
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("memory: mkdir %s: %w", dir, err)
	}
	// シンボリックリンク経由でディレクトリ外を指す構成を初期化時点で解決して固定する
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("memory: resolve %s: %w", dir, err)
	}
	return &Store{dir: resolved}, nil
}

// resolve rel を検証してディレクトリ内の絶対パスを返す。
// 受け付けるのはディレクトリ直下のフラットな `<name>.md` のみ
func (s *Store) resolve(rel string) (string, error) {
	if rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("memory: invalid path %q", rel)
	}
	if strings.ContainsRune(rel, '/') || strings.ContainsRune(rel, '\\') {
		return "", fmt.Errorf("memory: nested path %q is not allowed", rel)
	}
	if filepath.Ext(rel) != ".md" {
		return "", fmt.Errorf("memory: only .md files are allowed, got %q", rel)
	}
	path := filepath.Join(s.dir, rel)
	// 既存エントリがシンボリックリンク・非通常ファイルなら読み書きとも拒否する
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("memory: %q is a symlink", rel)
		}
		if !fi.Mode().IsRegular() {
			return "", fmt.Errorf("memory: %q is not a regular file", rel)
		}
	}
	return path, nil
}

// Read rel の内容を最大 maxBytes (rune 境界) まで読んで返す
func (s *Store) Read(rel string, maxBytes int) (string, error) {
	path, err := s.resolve(rel)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("memory: open %q: %w", rel, err)
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if maxBytes > 0 {
		r = io.LimitReader(f, int64(maxBytes)+1)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("memory: read %q: %w", rel, err)
	}
	return truncateAtRuneBoundary(string(b), maxBytes), nil
}

// Write rel へ content を書く。appendMode が true なら追記する。
// 書き込み後のファイルサイズが上限を超える場合は ErrMemoryFileTooLarge を返す
func (s *Store) Write(rel, content string, appendMode bool) error {
	path, err := s.resolve(rel)
	if err != nil {
		return err
	}
	size := len(content)
	if appendMode {
		if fi, statErr := os.Stat(path); statErr == nil {
			size += int(fi.Size())
		}
	}
	if size > maxMemoryFileBytes {
		return fmt.Errorf("%w (got %d)", ErrMemoryFileTooLarge, size)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open %q: %w", rel, err)
	}
	if _, werr := f.WriteString(content); werr != nil {
		_ = f.Close()
		return fmt.Errorf("memory: write %q: %w", rel, werr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("memory: close %q: %w", rel, cerr)
	}
	return nil
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
		if len(lines) > maxLines {
			content = strings.Join(lines[:maxLines], "")
		}
	}
	return content, nil
}

// List ディレクトリ直下の .md 通常ファイル名をソート済みで返す
func (s *Store) List() ([]string, error) {
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
// をパス区切りを `-` へ置換した文字列として返す。worktree の `.git` ファイルは
// `gitdir:` 参照を解決して主リポジトリのルートへ寄せる
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

// resolveGitFile worktree の `.git` ファイルから主リポジトリのルートを解決する。
// `gitdir: <main>/.git/worktrees/<name>` 形式を想定し、`.git` より前を返す
func resolveGitFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxGitFileBytes))
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(b))
	gitdir, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", false
	}
	gitdir = filepath.Clean(strings.TrimSpace(gitdir))
	sep := string(filepath.Separator)
	marker := sep + ".git" + sep
	if i := strings.Index(gitdir, marker); i >= 0 {
		return gitdir[:i], true
	}
	return "", false
}

// sanitizeKey 絶対パスをディレクトリ名として安全な 1 セグメントへ変換する
func sanitizeKey(path string) string {
	replaced := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '-'
		}
		return r
	}, path)
	return strings.Trim(replaced, "-")
}

// truncateAtRuneBoundary s が maxBytes を超える場合に rune 境界で切り詰める。
// memory.go とは独立に automemory 系だけで使う
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
