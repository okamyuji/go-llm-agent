package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sandbox 許可パスの管理
type Sandbox struct {
	allowedRoots []string
}

// NewSandbox 許可ルート群から Sandbox を生成する
func NewSandbox(roots []string) *Sandbox {
	clean := make([]string, 0, len(roots))
	for _, r := range roots {
		r = expandTilde(r)
		abs, err := filepath.Abs(r)
		if err == nil {
			clean = append(clean, abs)
		}
	}
	return &Sandbox{allowedRoots: clean}
}

// CheckPath path が許可ルート配下であるか確認する
func (s *Sandbox) CheckPath(path string) error {
	path = expandTilde(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for _, r := range s.allowedRoots {
		if abs == r || strings.HasPrefix(abs, r+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("sandbox: パス %q は許可ルート外", abs)
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
