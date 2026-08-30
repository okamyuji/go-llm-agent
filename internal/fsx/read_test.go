package fsx_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/fsx"
)

func TestTruncateAtRuneBoundary(t *testing.T) {
	cases := []struct {
		name     string
		s        string
		maxBytes int
		want     string
	}{
		{"上限0は無制限", "abc", 0, "abc"},
		{"負の上限も無制限", "abc", -1, "abc"},
		{"上限ちょうどはそのまま", "abcd", 4, "abcd"},
		{"上限ちょうどで末尾が不正バイトでも削らない", "a\xff", 2, "a\xff"},
		{"1バイト超過で末尾を落とす", "abcd", 3, "abc"},
		{"3バイト文字の途中で切らない", "ああ", 4, "あ"},
		{"3バイト文字の途中(2バイト残り)でも切らない", "ああ", 5, "あ"},
		{"先頭の不正バイトは残す", "\xffab", 2, "\xffa"},
		{"切り詰め位置が不正バイト単体なら削る", "a\xff\xff", 2, "a"},
		{"全部不正バイトなら空になる", "\xff\xff", 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fsx.TruncateAtRuneBoundary(tc.s, tc.maxBytes); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadCapped(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		maxBytes int
		want     string
	}{
		{"上限0は全文", "abcdef", 0, "abcdef"},
		{"上限ちょうどは全文", "abcd", 4, "abcd"},
		{"上限超過は切り詰め", "abcd", 3, "abc"},
		{"rune境界で切り詰め", "ああ", 4, "あ"},
		{"空ファイル", "", 4, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fsx.ReadCapped(writeTemp(t, tc.content), tc.maxBytes)
			if err != nil {
				t.Fatalf("ReadCapped: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadCapped_MissingFile(t *testing.T) {
	if _, err := fsx.ReadCapped(filepath.Join(t.TempDir(), "missing.md"), 10); err == nil {
		t.Fatalf("存在しないファイルでエラーにならない")
	}
}
