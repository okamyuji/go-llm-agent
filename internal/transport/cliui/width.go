package cliui

import "golang.org/x/text/width"

// runeWidth は端末上での表示桁数を返す。
// East Asian Wide / Fullwidth (日本語など) は 2、制御文字は 0、その他は 1。
// cooked モードでは端末任せだった桁計算を、raw モードでは自前で行うために使う。
func runeWidth(r rune) int {
	if r < 0x20 || r == 0x7f {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	default:
		return 1
	}
}

// stringWidth は文字列の表示桁数の合計を返す。
func stringWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// runesWidth は rune スライスの表示桁数の合計を返す。
func runesWidth(rs []rune) int {
	w := 0
	for _, r := range rs {
		w += runeWidth(r)
	}
	return w
}
