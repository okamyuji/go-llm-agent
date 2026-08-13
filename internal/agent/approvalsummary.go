package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// maxApprovalSummaryLines 承認プロンプトへ表示するサマリの最大行数
const maxApprovalSummaryLines = 200

// BuildApprovalSummary は承認プロンプト表示用サマリを組み立てる。
//   - fs_write: 対象パスの現在内容 (SummaryReader で取得、失敗時は空文字列=新規ファイル扱い) と
//     引数 content の UnifiedDiff
//   - fs_edit: old_string と new_string の UnifiedDiff (ファイル全体ではなく置換ブロックのみ)
//   - 上記以外: 引数 JSON を整形したもの
//
// いずれも maxApprovalSummaryLines で切り詰める。引数の unmarshal に失敗した場合は
// "(引数の解析に失敗しました: <err>)" を返す。サマリ生成の失敗で承認フロー自体を止めない
func BuildApprovalSummary(ctx context.Context, tools tool.Registry, toolName string, args json.RawMessage) string {
	switch toolName {
	case "fs_write":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return approvalArgsParseError(err)
		}
		return UnifiedDiff(fsWriteCurrentContent(ctx, tools, a.Path), a.Content, a.Path, maxApprovalSummaryLines)
	case "fs_edit":
		var a struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return approvalArgsParseError(err)
		}
		return UnifiedDiff(a.OldString, a.NewString, a.Path, maxApprovalSummaryLines)
	default:
		return formatApprovalArgs(args)
	}
}

// approvalArgsParseError 引数の解析失敗を承認者へ伝える表示文
func approvalArgsParseError(err error) string {
	return fmt.Sprintf("(引数の解析に失敗しました: %v)", err)
}

// formatApprovalArgs 任意ツールの引数 JSON を整形して行数上限で切り詰める
func formatApprovalArgs(args json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, args, "", "  "); err != nil {
		return approvalArgsParseError(err)
	}
	return truncateSummaryLines(buf.String(), maxApprovalSummaryLines)
}

// truncateSummaryLines 行数が maxLines を超える場合に先頭のみ残し切詰め行を加える
func truncateSummaryLines(s string, maxLines int) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) <= maxLines {
		return s
	}
	kept := lines[:maxLines:maxLines]
	kept = append(kept, fmt.Sprintf("…[truncated: %d further lines omitted]…", len(lines)-maxLines))
	return strings.Join(kept, "\n") + "\n"
}

// fsWriteCurrentContent fs_write 対象パスの現在内容を read registry を汚さずに読む。
// Tool.Execute を呼ばないのは、(1) 承認を拒否したパスが既読登録され「未読ファイルへの
// fs_edit は拒否」が回避可能になること、(2) required_tools に fs_read を含む構成で
// 承認対象ツールが承認を経ずに実行されることを防ぐため
func fsWriteCurrentContent(ctx context.Context, tools tool.Registry, path string) string {
	t, ok := tools.Lookup("fs_read")
	if !ok {
		return ""
	}
	// 型アサートに失敗した場合は内容取得を諦め、差分は「全行追加」として表示する
	sr, ok := t.(tool.SummaryReader)
	if !ok {
		return ""
	}
	content, err := sr.ReadForSummary(ctx, path)
	if err != nil {
		return "" // 新規ファイル、または読み取り不可
	}
	return content
}
