package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

var errSearchMaxResults = errors.New("search: max results reached")

// SearchFilesTool search_files ツールの実装
type SearchFilesTool struct {
	sb  *Sandbox
	cfg config.SearchFilesConfig
}

// NewSearchFiles Sandbox と config から SearchFilesTool を生成する
func NewSearchFiles(sb *Sandbox, cfg config.SearchFilesConfig) *SearchFilesTool {
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = 200
	}
	return &SearchFilesTool{sb: sb, cfg: cfg}
}

// Spec ツール定義を返す
func (t *SearchFilesTool) Spec() Spec {
	return Spec{
		Name:        "search_files",
		Description: "サンドボックス配下を再帰的に検索し regex にマッチした行のリストを返す",
		Schema: json.RawMessage(`{
"type":"object",
"properties":{
"root":{"type":"string"},
"pattern":{"type":"string"},
"globs":{"type":"array","items":{"type":"string"}}
},
"required":["root","pattern"]
}`),
	}
}

type searchArgs struct {
	Root    string   `json:"root"`
	Pattern string   `json:"pattern"`
	Globs   []string `json:"globs"`
}

// Execute root 配下を pattern (regex) で grep する
func (t *SearchFilesTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a searchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if a.Root == "" || a.Pattern == "" {
		return Result{IsError: true, Content: "root and pattern are required"}, nil
	}
	if err := t.sb.CheckPath(a.Root); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	gitIgnore := loadGitIgnore(a.Root)

	var out []string
	truncated := false
	walkErr := filepath.WalkDir(a.Root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			if gitIgnore.match(path, a.Root, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if gitIgnore.match(path, a.Root, false) {
			return nil
		}
		if len(a.Globs) > 0 {
			matched := false
			for _, g := range a.Globs {
				ok, _ := filepath.Match(g, d.Name())
				if ok {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		ln := 0
		for sc.Scan() {
			ln++
			line := sc.Text()
			if re.MatchString(line) {
				if len(out) >= t.cfg.MaxResults {
					truncated = true
					return errSearchMaxResults
				}
				out = append(out, fmt.Sprintf("%s:%d:%s", path, ln, line))
			}
		}
		_ = sc.Err()
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errSearchMaxResults) && ctx.Err() == nil {
		return Result{IsError: true, Content: walkErr.Error()}, nil
	}
	return Result{Content: strings.Join(out, "\n"), Truncated: truncated}, nil
}

type gitIgnoreSet struct {
	patterns []string
}

func loadGitIgnore(root string) gitIgnoreSet {
	b, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return gitIgnoreSet{}
	}
	lines := strings.Split(string(b), "\n")
	pats := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		pats = append(pats, l)
	}
	return gitIgnoreSet{patterns: pats}
}

func (g gitIgnoreSet) match(path, root string, _ bool) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	for _, p := range g.patterns {
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(p, base); ok {
			return true
		}
	}
	return false
}
