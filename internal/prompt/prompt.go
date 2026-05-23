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
// name と version はパスセパレータや ".." を含む値を受け付けない（path traversal 対策）
func (f *fileLoader) Load(ref string) (Template, error) {
	if ref == "" {
		return Template{}, errors.New("prompt: empty ref")
	}
	name, version, _ := strings.Cut(ref, "@")
	if name == "" {
		return Template{}, fmt.Errorf("prompt: ref must include name, got %q", ref)
	}
	if !isSafeComponent(name) || (version != "" && !isSafeComponent(version)) {
		return Template{}, fmt.Errorf("prompt: ref contains forbidden characters got=%q", ref)
	}
	var path string
	if version != "" {
		path = filepath.Join(f.dir, fmt.Sprintf("%s@%s.tmpl", name, version))
	} else {
		path = filepath.Join(f.dir, name+".tmpl")
	}
	// 念のため解決後のパスが dir 配下に収まることも検査する
	// filepath.EvalSymlinks も併用してシンボリックリンク経由で loader dir 外へ抜ける経路を遮断する
	// テンプレートファイルがまだ存在しない場合は EvalSymlinks がエラーになるため、
	// Abs ベースの prefix チェックに自動でフォールバックする
	cleanedDir, errDir := filepath.Abs(f.dir)
	cleanedPath, errPath := filepath.Abs(path)
	if errDir != nil || errPath != nil {
		return Template{}, fmt.Errorf("prompt: resolve path failed: %v / %v", errDir, errPath)
	}
	if resolved, err := filepath.EvalSymlinks(cleanedDir); err == nil {
		cleanedDir = resolved
	}
	if resolved, err := filepath.EvalSymlinks(cleanedPath); err == nil {
		cleanedPath = resolved
	}
	if !strings.HasPrefix(cleanedPath, cleanedDir+string(filepath.Separator)) && cleanedPath != cleanedDir {
		return Template{}, fmt.Errorf("prompt: path escapes loader dir got=%q", path)
	}
	// 解決後の cleanedPath を実際に読む。元の path を読むと
	// EvalSymlinks による検査をすり抜けるシンボリックリンクから別ファイルを読まされ得る
	b, err := os.ReadFile(cleanedPath)
	if err != nil {
		return Template{}, fmt.Errorf("prompt read %s: %w", cleanedPath, err)
	}
	return Template{Name: name, Version: version, Body: string(b)}, nil
}

// isSafeComponent name または version のコンポーネントが安全か検査する
// path separator / 親ディレクトリ参照 / NUL 文字を排除する
func isSafeComponent(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if r == '/' || r == '\\' || r == 0 {
			return false
		}
	}
	return true
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
//
// SSTI 注意 text/template の Funcs を本実装では絶対に登録しない
// FuncMap を追加すると {{call .Func arg}} 構文経由で任意関数が呼ばれ得るため、
// テンプレートファイル書き換え権限を持つ攻撃者に対してコード実行に直結する
// 信頼境界外からテンプレート本体を受け取らない運用 (loader dir の write 権限を制限) を前提とする
func (r *renderer) Render(t Template, vars map[string]any) (string, error) {
	if len(r.allowed) > 0 {
		for k := range vars {
			if !r.allowed[k] {
				return "", fmt.Errorf("prompt: variable %q is not in allowlist", k)
			}
		}
	}
	// Funcs を空 FuncMap で固定 将来の追加変更を SSTI レビューの起点にする
	tmpl, err := template.New(t.Ref()).Funcs(template.FuncMap{}).Option("missingkey=error").Parse(t.Body)
	if err != nil {
		return "", fmt.Errorf("prompt parse: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("prompt execute: %w", err)
	}
	return buf.String(), nil
}
