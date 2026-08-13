package agent

import "fmt"

// TruncateToolResult content が maxChars (rune 数) を超える場合、
// 先頭 60%・末尾 40% を残して中央を省略マーカーへ置換した文字列を返す。
// maxChars <= 0 のときは切り詰めを無効化し content をそのまま返す。
// content の rune 数が maxChars 以下のときも content をそのまま返す。
// 分割は []rune 単位で行うため UTF-8 のマルチバイト文字を分断しない。
func TruncateToolResult(content string, maxChars int) string {
	if maxChars <= 0 {
		return content
	}
	runes := []rune(content)
	total := len(runes)
	if total <= maxChars {
		return content
	}

	headChars := maxChars * 60 / 100
	tailChars := maxChars - headChars
	omitted := total - headChars - tailChars

	head := string(runes[:headChars])
	tail := string(runes[total-tailChars:])
	marker := fmt.Sprintf("…[truncated: %d chars omitted]…", omitted)
	return head + marker + tail
}
