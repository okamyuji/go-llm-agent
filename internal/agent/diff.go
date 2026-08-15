package agent

import (
	"fmt"
	"strings"
)

// maxDiffInputLines UnifiedDiff が LCS を計算する入力の行数上限。
// oldText または newText のどちらかがこれを超える場合、LCS を計算せず
// 行数のみのサマリを返す。LCS は O(len(oldLines)*len(newLines)) であり、
// fs_write の content はモデル生成の引数、既存内容は fs_read の MaxReadBytes
// までの任意サイズで、どちらにも行数上限が掛かっていない。この計算は
// approval の timeout_seconds が進行する承認ウィンドウの内側で走るため、
// 上限を設けないと承認待ち時間を計算だけで食い潰しうる
const maxDiffInputLines = 2000

// UnifiedDiff は oldText と newText から人間可読な簡略 unified diff を生成する。
// git apply 互換ではなく、承認プロンプト表示専用 (@@ hunk ヘッダは出力しない)。
// 共通行は " "、削除行は "-"、追加行は "+" を先頭に付ける。
// 入力のいずれかが maxDiffInputLines 行を超える場合は LCS を計算せず行数だけを示す。
// 出力行数が maxLines を超える場合は先頭 maxLines 行のみ残し切詰め行を 1 行加える
func UnifiedDiff(oldText, newText, path string, maxLines int) string {
	oldLines := splitDiffLines(oldText)
	newLines := splitDiffLines(newText)
	header := []string{"--- a/" + path, "+++ b/" + path}
	if len(oldLines) > maxDiffInputLines || len(newLines) > maxDiffInputLines {
		skipped := fmt.Sprintf("…[diff skipped: old %d lines, new %d lines (over %d-line limit)]…",
			len(oldLines), len(newLines), maxDiffInputLines)
		return joinDiffLines(append(header, skipped))
	}
	lines := header
	lines = append(lines, renderDiffBody(oldLines, newLines)...)
	if len(lines) > maxLines {
		omitted := len(lines) - maxLines
		lines = lines[:maxLines:maxLines]
		lines = append(lines, fmt.Sprintf("…[diff truncated: %d further lines omitted]…", omitted))
	}
	return joinDiffLines(lines)
}

// splitDiffLines テキストを行へ分ける。末尾の改行は行を増やさない。空文字は 0 行
func splitDiffLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func joinDiffLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// renderDiffBody 行ベース LCS から差分行を組み立てる
func renderDiffBody(oldLines, newLines []string) []string {
	table := lcsTable(oldLines, newLines)
	out := make([]string, 0, len(oldLines)+len(newLines))
	width := len(newLines) + 1
	i, j := 0, 0
	for i < len(oldLines) && j < len(newLines) {
		switch {
		case oldLines[i] == newLines[j]:
			out = append(out, " "+oldLines[i])
			i++
			j++
		case table[(i+1)*width+j] >= table[i*width+j+1]:
			out = append(out, "-"+oldLines[i])
			i++
		default:
			out = append(out, "+"+newLines[j])
			j++
		}
	}
	for ; i < len(oldLines); i++ {
		out = append(out, "-"+oldLines[i])
	}
	for ; j < len(newLines); j++ {
		out = append(out, "+"+newLines[j])
	}
	return out
}

// lcsTable 最長共通部分列の長さ表を平坦なスライスで返す。
// table[i*width+j] は oldLines[i:] と newLines[j:] の LCS 長 (width = len(newLines)+1)
func lcsTable(oldLines, newLines []string) []int {
	width := len(newLines) + 1
	table := make([]int, (len(oldLines)+1)*width)
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				table[i*width+j] = table[(i+1)*width+j+1] + 1
				continue
			}
			table[i*width+j] = max(table[(i+1)*width+j], table[i*width+j+1])
		}
	}
	return table
}
