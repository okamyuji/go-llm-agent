package secret_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/secret"
)

func TestMaskMap_KeyLikeFieldsAreMasked(t *testing.T) {
	in := map[string]string{
		"OPENAI_API_KEY":  "sk-real-12345",
		"ANTHROPIC_TOKEN": "sk-ant-aaa",
		"USER_NAME":       "okamyuji",
	}
	out := secret.MaskMap(in)
	if out["OPENAI_API_KEY"] != "***" {
		t.Fatalf("API_KEY を *** にすること got=%q", out["OPENAI_API_KEY"])
	}
	if out["ANTHROPIC_TOKEN"] != "***" {
		t.Fatalf("TOKEN を *** にすること got=%q", out["ANTHROPIC_TOKEN"])
	}
	if out["USER_NAME"] != "okamyuji" {
		t.Fatalf("一般フィールドはマスクしないこと got=%q", out["USER_NAME"])
	}
}

func TestMaskString(t *testing.T) {
	if secret.MaskString("sk-12345") != "***" {
		t.Fatalf("sk- 始まりは masked")
	}
	if secret.MaskString("hello") != "hello" {
		t.Fatalf("非シークレットは素通し")
	}
}

func TestResolveFromEnv(t *testing.T) {
	t.Setenv("X_API_KEY", "real")
	r := secret.NewResolver("")
	v, err := r.Resolve("X_API_KEY")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if v != "real" {
		t.Fatalf("env を返すこと got=%q", v)
	}
}

func TestResolveFromDotenv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("Y_TEST_VAR=fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := secret.NewResolver(path)
	v, err := r.Resolve("Y_TEST_VAR")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if v != "fromfile" {
		t.Fatalf(".env を返すこと got=%q", v)
	}
}

func TestResolveMissing(t *testing.T) {
	r := secret.NewResolver("")
	_, err := r.Resolve("NOT_SET_XYZ_VAR")
	if err == nil {
		t.Fatalf("未設定でエラーを返すこと")
	}
}

func TestResolveAnyUsesFallback(t *testing.T) {
	t.Setenv("PRIMARY_TEST_KEY", "")
	t.Setenv("FALLBACK_TEST_KEY", "fallback")
	r := secret.NewResolver("")
	v, used, err := secret.ResolveAny(r, "PRIMARY_TEST_KEY", "FALLBACK_TEST_KEY")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if v != "fallback" || used != "FALLBACK_TEST_KEY" {
		t.Fatalf("fallback value/name mismatch value=%q used=%q", v, used)
	}
}

func TestResolveAnyMissing(t *testing.T) {
	r := secret.NewResolver("")
	_, _, err := secret.ResolveAny(r, "MISSING_ONE", "MISSING_TWO")
	if err == nil {
		t.Fatalf("expected error when all candidates are missing")
	}
}
