package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestUnifiedDiff_NewFile(t *testing.T) {
	got := UnifiedDiff("", "a\nb\n", "f.txt", 200)
	if !strings.Contains(got, "--- a/f.txt") || !strings.Contains(got, "+++ b/f.txt") {
		t.Fatalf("ヘッダ期待 got %q", got)
	}
	for _, want := range []string{"+a", "+b"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q を含む期待 got %q", want, got)
		}
	}
	if strings.Contains(got, "\n-") {
		t.Fatalf("新規ファイルに削除行は出ない期待 got %q", got)
	}
}

func TestUnifiedDiff_ModifiedLines(t *testing.T) {
	got := UnifiedDiff("a\nb\nc\n", "a\nB\nc\n", "f.txt", 200)
	for _, want := range []string{" a", "-b", "+B", " c"} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("%q 行を含む期待 got %q", want, got)
		}
	}
}

func TestUnifiedDiff_Identical(t *testing.T) {
	got := UnifiedDiff("a\nb\n", "a\nb\n", "f.txt", 200)
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			t.Fatalf("削除行が出てはならない got %q", got)
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			t.Fatalf("追加行が出てはならない got %q", got)
		}
	}
}

func TestUnifiedDiff_TruncatesAtMaxLines(t *testing.T) {
	var b strings.Builder
	for i := range 300 {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	got := UnifiedDiff("", b.String(), "f.txt", 200)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 201 {
		t.Fatalf("先頭 200 行 + 切詰め行 1 行期待 got %d", len(lines))
	}
	if !strings.Contains(lines[200], "diff truncated") {
		t.Fatalf("切詰め行期待 got %q", lines[200])
	}
}

func TestUnifiedDiff_OverInputLineLimit_SkipsLCS(t *testing.T) {
	var b strings.Builder
	for i := range maxDiffInputLines + 1 {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	start := time.Now()
	got := UnifiedDiff("a\n", b.String(), "f.txt", 200)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("LCS を計算せず即座に返す期待 got %s", elapsed)
	}
	if !strings.Contains(got, "diff skipped") {
		t.Fatalf("skip メッセージ期待 got %q", got)
	}
	if !strings.Contains(got, "old 1 lines") || !strings.Contains(got, fmt.Sprintf("new %d lines", maxDiffInputLines+1)) {
		t.Fatalf("両者の行数を含む期待 got %q", got)
	}
}

func TestUnifiedDiff_AtInputLineLimit_ComputesDiff(t *testing.T) {
	var old, updated strings.Builder
	for i := range maxDiffInputLines {
		fmt.Fprintf(&old, "line%d\n", i)
		if i == 0 {
			updated.WriteString("changed\n")
			continue
		}
		fmt.Fprintf(&updated, "line%d\n", i)
	}
	got := UnifiedDiff(old.String(), updated.String(), "f.txt", 200)
	if strings.Contains(got, "diff skipped") {
		t.Fatalf("境界値では diff を計算する期待 got %q", got)
	}
	if !strings.Contains(got, "-line0") || !strings.Contains(got, "+changed") {
		t.Fatalf("変更行が出る期待 got %q", got)
	}
}

func TestUnifiedDiff_EmptyBoth(t *testing.T) {
	got := UnifiedDiff("", "", "f.txt", 200)
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("ヘッダ 2 行のみ期待 got %q", got)
	}
}
