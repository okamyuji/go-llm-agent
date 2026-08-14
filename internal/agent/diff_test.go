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

func TestUnifiedDiff_MaxLinesBoundary(t *testing.T) {
	tests := []struct {
		name     string
		maxLines int
		want     string
	}{
		{
			name:     "出力行数がちょうど maxLines なら切り詰めない",
			maxLines: 4,
			want:     "--- a/f.txt\n+++ b/f.txt\n+a\n+b\n",
		},
		{
			name:     "1 行超過なら omitted は 1",
			maxLines: 3,
			want:     "--- a/f.txt\n+++ b/f.txt\n+a\n…[diff truncated: 1 further lines omitted]…\n",
		},
		{
			name:     "2 行超過なら omitted は 2",
			maxLines: 2,
			want:     "--- a/f.txt\n+++ b/f.txt\n…[diff truncated: 2 further lines omitted]…\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnifiedDiff("", "a\nb\n", "f.txt", tc.maxLines); got != tc.want {
				t.Fatalf("want %q got %q", tc.want, got)
			}
		})
	}
}

func TestRenderDiffBody(t *testing.T) {
	tests := []struct {
		name string
		old  []string
		new  []string
		want []string
	}{
		{
			name: "先頭への追加は追加行が先に出る",
			old:  []string{"b"},
			new:  []string{"a", "b"},
			want: []string{"+a", " b"},
		},
		{
			name: "共通行が無い置換は削除行が先に出る",
			old:  []string{"a"},
			new:  []string{"b"},
			want: []string{"-a", "+b"},
		},
		{
			name: "末尾削除は new を消費し切った後の残りとして出る",
			old:  []string{"a", "b"},
			new:  []string{"a"},
			want: []string{" a", "-b"},
		},
		{
			name: "末尾追加は old を消費し切った後の残りとして出る",
			old:  []string{"a"},
			new:  []string{"a", "b"},
			want: []string{" a", "+b"},
		},
		{
			name: "順序入れ替えは LCS の長い方を共通行にする",
			old:  []string{"a", "p", "q"},
			new:  []string{"p", "a", "q"},
			want: []string{"-a", " p", "+a", " q"},
		},
		{
			name: "両方空なら 0 行",
			old:  nil,
			new:  nil,
			want: []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderDiffBody(tc.old, tc.new)
			if len(got) != len(tc.want) {
				t.Fatalf("want %q got %q", tc.want, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("want %q got %q", tc.want, got)
				}
			}
		})
	}
}

func TestLCSTable(t *testing.T) {
	tests := []struct {
		name string
		old  []string
		new  []string
		want []int
	}{
		{
			name: "順序入れ替え",
			old:  []string{"a", "p", "q"},
			new:  []string{"p", "a", "q"},
			want: []int{
				2, 2, 1, 0,
				2, 1, 1, 0,
				1, 1, 1, 0,
				0, 0, 0, 0,
			},
		},
		{
			name: "先頭削除と末尾追加",
			old:  []string{"a", "b", "c"},
			new:  []string{"b", "c", "d"},
			want: []int{
				2, 1, 0, 0,
				2, 1, 0, 0,
				1, 1, 0, 0,
				0, 0, 0, 0,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lcsTable(tc.old, tc.new)
			if len(got) != len(tc.want) {
				t.Fatalf("want %v got %v", tc.want, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("want %v got %v", tc.want, got)
				}
			}
		})
	}
}

// lcsLenRef renderDiffBody とは独立に LCS 長を求める参照実装 (前向き DP)。
// 本体の後ろ向きテーブルと添字の組み立てを共有しないことが検証の前提
func lcsLenRef(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
				continue
			}
			dp[i][j] = max(dp[i-1][j], dp[i][j-1])
		}
	}
	return dp[len(a)][len(b)]
}

// 長さ 4 までの全入力対について renderDiffBody の不変条件を網羅的に検査する。
// テーブル添字の組み立て (行 stride・列オフセット) が 1 つでも狂うと貪欲な
// 選択が最長共通部分列を外し、文脈行数が LCS 長を下回って露見する
func TestRenderDiffBody_InvariantsExhaustive(t *testing.T) {
	alphabet := []string{"a", "b", "c"}
	var seqs [][]string
	var build func(cur []string)
	build = func(cur []string) {
		seqs = append(seqs, append([]string(nil), cur...))
		if len(cur) == 4 {
			return
		}
		for _, r := range alphabet {
			build(append(cur, r))
		}
	}
	build(nil)

	for _, old := range seqs {
		for _, updated := range seqs {
			got := renderDiffBody(old, updated)
			var ctx, gotOld, gotNew []string
			for _, line := range got {
				switch line[0] {
				case ' ':
					ctx = append(ctx, line[1:])
					gotOld = append(gotOld, line[1:])
					gotNew = append(gotNew, line[1:])
				case '-':
					gotOld = append(gotOld, line[1:])
				case '+':
					gotNew = append(gotNew, line[1:])
				default:
					t.Fatalf("接頭辞は空白/-/+ のいずれか old=%v new=%v got=%v", old, updated, got)
				}
			}
			if !slicesEqual(gotOld, old) {
				t.Fatalf("文脈行+削除行は旧内容を再構成する期待 old=%v new=%v got=%v", old, updated, got)
			}
			if !slicesEqual(gotNew, updated) {
				t.Fatalf("文脈行+追加行は新内容を再構成する期待 old=%v new=%v got=%v", old, updated, got)
			}
			if want := lcsLenRef(old, updated); len(ctx) != want {
				t.Fatalf("文脈行数は LCS 長 %d 期待 got %d (old=%v new=%v got=%v)", want, len(ctx), old, updated, got)
			}
		}
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
