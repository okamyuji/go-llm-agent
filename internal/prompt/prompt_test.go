package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemplate(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoader_LoadByVersionedName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTemplate(t, dir, "refund@v3.tmpl", "hello {{.user}}")
	l := NewFileLoader(dir)
	got, err := l.Load("refund@v3")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "refund" || got.Version != "v3" {
		t.Errorf("got name=%q version=%q", got.Name, got.Version)
	}
}

func TestLoader_LoadFallsBackToBareName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTemplate(t, dir, "system.tmpl", "you are an assistant")
	l := NewFileLoader(dir)
	got, err := l.Load("system")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "" {
		t.Errorf("version must be empty, got %q", got.Version)
	}
}

func TestLoader_EmptyRefErrors(t *testing.T) {
	t.Parallel()
	l := NewFileLoader(t.TempDir())
	if _, err := l.Load(""); err == nil {
		t.Fatal("expected error for empty ref")
	}
}

func TestLoader_MissingFileErrors(t *testing.T) {
	t.Parallel()
	l := NewFileLoader(t.TempDir())
	if _, err := l.Load("nope@v1"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestRenderer_RendersWithAllowedVars(t *testing.T) {
	t.Parallel()
	r := NewRenderer([]string{"user", "now"})
	out, err := r.Render(Template{Body: "hello {{.user}} at {{.now}}"}, map[string]any{"user": "alice", "now": "2026"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello alice at 2026" {
		t.Errorf("got %q", out)
	}
}

func TestRenderer_RejectsDisallowedVar(t *testing.T) {
	t.Parallel()
	r := NewRenderer([]string{"user"})
	_, err := r.Render(Template{Body: "x"}, map[string]any{"secret": "boom"})
	if err == nil {
		t.Fatal("expected error for disallowed variable")
	}
}

func TestRenderer_EmptyAllowlistMeansAll(t *testing.T) {
	t.Parallel()
	r := NewRenderer(nil)
	out, err := r.Render(Template{Body: "{{.x}}"}, map[string]any{"x": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Errorf("got %q", out)
	}
}

func TestRenderer_MissingKeyErrors(t *testing.T) {
	t.Parallel()
	r := NewRenderer(nil)
	_, err := r.Render(Template{Body: "{{.missing}}"}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

// TestLoader_RejectsPathTraversalRefs name/version 部分への path traversal や
// 制御文字混入を Loader が事前に弾くことを確認する
func TestLoader_RejectsPathTraversalRefs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := NewFileLoader(dir)
	cases := []string{
		"../secret",
		"..",
		".",
		"foo/bar",
		"foo\\bar",
		"foo\x00bar",
		"system@../v1",
		"system@v1/../etc/passwd",
	}
	for _, ref := range cases {
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			if _, err := l.Load(ref); err == nil {
				t.Fatalf("ref=%q must be rejected", ref)
			}
		})
	}
}
