// Package prompt プロンプトテンプレートのファイルベース版管理を提供する
// 命名規約は <name>@<version>.tmpl で、変数は許可リストにあるキーのみ展開する
package prompt

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// Template ロード済みテンプレートの本体
type Template struct {
	Name    string
	Version string
	Body    string
}

// Ref name@version 形式の参照
func (t Template) Ref() string {
	if t.Version == "" {
		return t.Name
	}
	return t.Name + "@" + t.Version
}

// Loader テンプレートを読み込む
type Loader interface {
	Load(ref string) (Template, error)
}

// fileLoader ファイルから *.tmpl をロードする
type fileLoader struct {
	dir string
}

// NewFileLoader 指定ディレクトリの Loader を返す
func NewFileLoader(dir string) Loader {
	return &fileLoader{dir: dir}
}

// Load name@version を <dir>/<name>@<version>.tmpl から読む
// version 省略時は <name>.tmpl にフォールバック
func (f *fileLoader) Load(ref string) (Template, error) {
	if ref == "" {
		return Template{}, errors.New("prompt: empty ref")
	}
	name, version, _ := strings.Cut(ref, "@")
	if name == "" {
		return Template{}, fmt.Errorf("prompt: ref must include name, got %q", ref)
	}
	var path string
	if version != "" {
		path = filepath.Join(f.dir, fmt.Sprintf("%s@%s.tmpl", name, version))
	} else {
		path = filepath.Join(f.dir, name+".tmpl")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Template{}, fmt.Errorf("prompt read %s: %w", path, err)
	}
	return Template{Name: name, Version: version, Body: string(b)}, nil
}

// Renderer テンプレートを展開する
type Renderer interface {
	Render(t Template, vars map[string]any) (string, error)
}

// renderer text/template ベースのレンダラ
type renderer struct {
	allowed map[string]bool
}

// NewRenderer 許可された変数キーのリストから Renderer を構築する
// allowedKeys が空のときは全変数を許可する
func NewRenderer(allowedKeys []string) Renderer {
	allow := make(map[string]bool, len(allowedKeys))
	for _, k := range allowedKeys {
		allow[k] = true
	}
	return &renderer{allowed: allow}
}

// Render Template.Body を text/template で展開する
// allowed が非空の場合、vars のキーがホワイトリストに含まれていなければエラー
func (r *renderer) Render(t Template, vars map[string]any) (string, error) {
	if len(r.allowed) > 0 {
		for k := range vars {
			if !r.allowed[k] {
				return "", fmt.Errorf("prompt: variable %q is not in allowlist", k)
			}
		}
	}
	tmpl, err := template.New(t.Ref()).Option("missingkey=error").Parse(t.Body)
	if err != nil {
		return "", fmt.Errorf("prompt parse: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("prompt execute: %w", err)
	}
	return buf.String(), nil
}
