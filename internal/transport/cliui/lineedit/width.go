package lineedit

import runewidth "github.com/mattn/go-runewidth"

// runeCells r の表示セル幅を返す。制御文字・結合文字は 0 とする。
// 負値を 0 へ丸めるのは、bracketed paste 中に制御文字が行へ入り得るため
// (isPrintable を経由しない経路がある) 桁計算を破綻させないためである。
func runeCells(r rune) int {
	w := runewidth.RuneWidth(r)
	if w < 0 {
		return 0
	}
	return w
}

// promptCells prompt 中のエスケープ列を取り除いた rune 列を返す。
// 戻り値は常に新規に確保したスライスであり、引数の部分スライスを返さない。
// 呼び出し側が結果へ append しても入力の backing array を破壊しないことを契約とする。
func promptCells(prompt []rune) []rune {
	out := make([]rune, 0, len(prompt))
	inEscapeSeq := false
	for _, r := range prompt {
		switch {
		case inEscapeSeq:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscapeSeq = false
			}
		case r == '\x1b':
			inEscapeSeq = true
		default:
			out = append(out, r)
		}
	}
	return out
}

// visualWidth エスケープ列を除いた表示セル幅を返す。
func visualWidth(runes []rune) int {
	total := 0
	for _, r := range promptCells(runes) {
		total += runeCells(r)
	}
	return total
}

// advanceCell 幅 w の rune を 1 つ書いたあとの画面座標を返す。
// 折返し規則と桁送りを writeLineCells・advanceCursor と共有する単一の実装。
// 桁送りを advanceCursor と同じ除算・剰余で書くことが必須である。1 回だけの
// 減算にすると、端末幅 1 で 2 セル文字を書いたときに両者の行数が食い違う。
func advanceCell(x, y, w, width int) (int, int) {
	if width < 1 {
		width = 1
	}
	if w > width-x { // 行末に収まらないのでパディングして折返す
		x, y = 0, y+1
	}
	x += w
	y += x / width
	x %= width
	return x, y
}

// cellPos prompt と text を writeLineCells と同一の折返し規則で配置したときの
// 画面座標を返す。x は 0 起点の桁、y は入力行の先頭行を 0 とする行番号。
// prompt と text を 2 つのループで順に走査する。append で 1 本のスライスへ
// 連結しないのは、promptCells の戻り値が余剰容量を持つ場合に append が
// 呼び出し側 (t.prompt) の backing array を上書きしうるためである。
func cellPos(prompt, text []rune, width int) (x, y int) {
	for _, r := range promptCells(prompt) {
		x, y = advanceCell(x, y, runeCells(r), width)
	}
	for _, r := range text {
		x, y = advanceCell(x, y, runeCells(r), width)
	}
	return x, y
}
