package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// stubSummaryRead fs_read の代わりに固定内容を返す SummaryReader 実装
type stubSummaryRead struct {
	content string
	err     error
	execs   *int
}

func (s stubSummaryRead) Spec() tool.Spec {
	return tool.Spec{Name: "fs_read", Description: "stub", Schema: json.RawMessage(`{"type":"object"}`)}
}

func (s stubSummaryRead) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	if s.execs != nil {
		(*s.execs)++
	}
	return tool.Result{Content: s.content}, nil
}

func (s stubSummaryRead) ReadForSummary(_ context.Context, _ string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.content, nil
}

// plainRead SummaryReader を実装しない fs_read 相当のツール
type plainRead struct{ execs *int }

func (p plainRead) Spec() tool.Spec {
	return tool.Spec{Name: "fs_read", Description: "stub", Schema: json.RawMessage(`{"type":"object"}`)}
}

func (p plainRead) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	if p.execs != nil {
		(*p.execs)++
	}
	return tool.Result{Content: "should not be used"}, nil
}

func TestBuildApprovalSummary_FSWriteNewFile(t *testing.T) {
	t.Parallel()
	tools := tool.NewRegistry([]tool.Tool{stubSummaryRead{err: os.ErrNotExist}}, []string{"fs_read"})
	got := BuildApprovalSummary(context.Background(), tools, "fs_write",
		json.RawMessage(`{"path":"/tmp/x.txt","content":"a\nb\n"}`))
	if !strings.Contains(got, "+a") || !strings.Contains(got, "+b") {
		t.Fatalf("新規ファイルは全行追加期待 got %q", got)
	}
	if strings.Contains(got, "\n-") {
		t.Fatalf("削除行は出ない期待 got %q", got)
	}
}

func TestBuildApprovalSummary_FSWriteExistingFile(t *testing.T) {
	t.Parallel()
	tools := tool.NewRegistry([]tool.Tool{stubSummaryRead{content: "a\nb\n"}}, []string{"fs_read"})
	got := BuildApprovalSummary(context.Background(), tools, "fs_write",
		json.RawMessage(`{"path":"/tmp/x.txt","content":"a\nB\n"}`))
	if !strings.Contains(got, "-b") || !strings.Contains(got, "+B") {
		t.Fatalf("差分行が出る期待 got %q", got)
	}
}

func TestBuildApprovalSummary_FSEdit(t *testing.T) {
	t.Parallel()
	got := BuildApprovalSummary(context.Background(), tool.NewRegistry(nil, nil), "fs_edit",
		json.RawMessage(`{"path":"/tmp/x.go","old_string":"old line","new_string":"new line"}`))
	if !strings.Contains(got, "-old line") || !strings.Contains(got, "+new line") {
		t.Fatalf("置換ブロックの差分期待 got %q", got)
	}
	if strings.Count(got, "\n") > 4 {
		t.Fatalf("ファイル全体ではなくブロック単体の差分期待 got %q", got)
	}
}

func TestBuildApprovalSummary_OtherTool(t *testing.T) {
	t.Parallel()
	got := BuildApprovalSummary(context.Background(), tool.NewRegistry(nil, nil), "shell",
		json.RawMessage(`{"command":"ls -la"}`))
	if !strings.Contains(got, `"command": "ls -la"`) {
		t.Fatalf("整形済み JSON 期待 got %q", got)
	}
}

func TestBuildApprovalSummary_MalformedArgs(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"fs_write", "fs_edit", "shell"} {
		got := BuildApprovalSummary(context.Background(), tool.NewRegistry(nil, nil), name, json.RawMessage(`{oops`))
		if !strings.Contains(got, "引数の解析に失敗しました") {
			t.Fatalf("%s: 解析失敗の表示期待 got %q", name, got)
		}
	}
}

func TestBuildApprovalSummary_TruncatesLongArgs(t *testing.T) {
	t.Parallel()
	values := make([]string, 0, 300)
	for i := range 300 {
		values = append(values, strings.Repeat("x", 1)+string(rune('a'+i%26)))
	}
	args, err := json.Marshal(map[string][]string{"items": values})
	if err != nil {
		t.Fatal(err)
	}
	got := BuildApprovalSummary(context.Background(), tool.NewRegistry(nil, nil), "shell", args)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != maxApprovalSummaryLines+1 {
		t.Fatalf("先頭 %d 行 + 切詰め行期待 got %d", maxApprovalSummaryLines, len(lines))
	}
	if !strings.Contains(lines[len(lines)-1], "truncated") {
		t.Fatalf("切詰め行期待 got %q", lines[len(lines)-1])
	}
}

func TestBuildApprovalSummary_FSReadNotSummaryReader(t *testing.T) {
	t.Parallel()
	execs := 0
	tools := tool.NewRegistry([]tool.Tool{plainRead{execs: &execs}}, []string{"fs_read"})
	got := BuildApprovalSummary(context.Background(), tools, "fs_write",
		json.RawMessage(`{"path":"/tmp/x.txt","content":"a\n"}`))
	if execs != 0 {
		t.Fatalf("Execute を呼んではならない got %d", execs)
	}
	if !strings.Contains(got, "+a") {
		t.Fatalf("空内容として全行追加の差分期待 got %q", got)
	}
}

func TestBuildApprovalSummary_NoFSReadRegistered(t *testing.T) {
	t.Parallel()
	got := BuildApprovalSummary(context.Background(), tool.NewRegistry(nil, nil), "fs_write",
		json.RawMessage(`{"path":"/tmp/x.txt","content":"a\n"}`))
	if !strings.Contains(got, "+a") {
		t.Fatalf("fs_read 未登録でも差分を返す期待 got %q", got)
	}
}

func TestBuildApprovalSummary_DoesNotPolluteReadRegistry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "target.txt")
	if werr := os.WriteFile(path, []byte("original\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	sb := tool.NewSandbox([]string{dir})
	reads := tool.NewReadRegistry()
	tools := tool.NewRegistry([]tool.Tool{
		tool.NewFSReadWithLogger(sb, 1<<20, slog.Default(), reads),
		tool.NewFSEdit(sb, reads, slog.Default()),
	}, []string{"fs_read", "fs_edit"})

	args, err := json.Marshal(map[string]string{"path": path, "content": "changed\n"})
	if err != nil {
		t.Fatal(err)
	}
	if got := BuildApprovalSummary(context.Background(), tools, "fs_write", args); !strings.Contains(got, "-original") {
		t.Fatalf("既存内容との差分期待 got %q", got)
	}

	editArgs, err := json.Marshal(map[string]string{"path": path, "old_string": "original", "new_string": "x"})
	if err != nil {
		t.Fatal(err)
	}
	edit, ok := tools.Lookup("fs_edit")
	if !ok {
		t.Fatal("fs_edit が登録されている期待")
	}
	res, execErr := edit.Execute(context.Background(), editArgs)
	if execErr == nil && !res.IsError {
		t.Fatal("サマリ生成は既読登録しないため fs_edit は拒否される期待")
	}
	msg := res.Content
	if execErr != nil {
		msg = execErr.Error()
	}
	if !strings.Contains(msg, "was not read in this session") {
		t.Fatalf("未読エラー期待 got %q", msg)
	}
}

func TestTruncateSummaryLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		s        string
		maxLines int
		want     string
	}{
		{
			name:     "行数がちょうど maxLines なら原文をそのまま返す",
			s:        "a\nb\nc\n",
			maxLines: 3,
			want:     "a\nb\nc\n",
		},
		{
			name:     "1 行超過なら omitted は 1",
			s:        "a\nb\nc\n",
			maxLines: 2,
			want:     "a\nb\n…[truncated: 1 further lines omitted]…\n",
		},
		{
			name:     "2 行超過なら omitted は 2",
			s:        "a\nb\nc\n",
			maxLines: 1,
			want:     "a\n…[truncated: 2 further lines omitted]…\n",
		},
		{
			name:     "maxLines 未満なら原文をそのまま返す",
			s:        "a\n",
			maxLines: 3,
			want:     "a\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := truncateSummaryLines(tc.s, tc.maxLines); got != tc.want {
				t.Fatalf("want %q got %q", tc.want, got)
			}
		})
	}
}
