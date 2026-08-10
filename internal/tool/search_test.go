package tool_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func TestSearch_FindsPattern(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("foo\nbar\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := tool.NewSandbox([]string{dir})
	s := tool.NewSearchFiles(sb, config.SearchFilesConfig{MaxResults: 100})
	res, _ := s.Execute(context.Background(), json.RawMessage(`{"root":"`+dir+`","pattern":"foo"}`))
	if res.IsError {
		t.Fatalf("err: %s", res.Content)
	}
	if !strings.Contains(res.Content, "foo") {
		t.Fatalf("content=%q", res.Content)
	}
}

func TestSearch_RejectsOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	sb := tool.NewSandbox([]string{dir})
	s := tool.NewSearchFiles(sb, config.SearchFilesConfig{MaxResults: 10})
	res, _ := s.Execute(context.Background(), json.RawMessage(`{"root":"/etc","pattern":"root"}`))
	if !res.IsError {
		t.Fatal("外側はエラー")
	}
}

func TestSearch_MaxResultsTruncates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte("x\nx\nx\nx\nx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := tool.NewSandbox([]string{dir})
	s := tool.NewSearchFiles(sb, config.SearchFilesConfig{MaxResults: 2})
	res, _ := s.Execute(context.Background(), json.RawMessage(`{"root":"`+dir+`","pattern":"x"}`))
	if !res.Truncated {
		t.Fatal("truncated true 期待")
	}
}

func TestSearch_SkipsSensitiveFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=match\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "safe.txt"), []byte("match\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb := tool.NewSandbox([]string{dir})
	s := tool.NewSearchFiles(sb, config.SearchFilesConfig{MaxResults: 10})
	res, _ := s.Execute(context.Background(), json.RawMessage(`{"root":"`+dir+`","pattern":"match"}`))
	if res.IsError {
		t.Fatalf("err: %s", res.Content)
	}
	if strings.Contains(res.Content, ".env") {
		t.Fatalf("強制deny対象を検索してはならない: %s", res.Content)
	}
	if !strings.Contains(res.Content, "safe.txt") {
		t.Fatalf("通常ファイルは検索対象: %s", res.Content)
	}
}
