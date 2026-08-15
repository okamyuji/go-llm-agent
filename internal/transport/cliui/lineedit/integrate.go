package lineedit

import "unicode/utf8"

// 本ファイルはフォーク基底部 (terminal.go) が呼び出す統合点を提供する。
// terminal.go 側は 1 行の呼び出し置換に留め、ロジックはここへ置く。

// queueRune は 1 rune を outBuf へ直接符号化する。queue と違い
// 中間の []rune / string を確保しないため、1 rune ずつ書き出す
// writeLineCells の確保コストが行長に比例して増えない。
func (t *Terminal) queueRune(r rune) {
	t.outBuf = utf8.AppendRune(t.outBuf, r)
}

// padding n 個の空白 rune を返す。
func padding(n int) []rune {
	if n <= 0 {
		return nil
	}
	s := make([]rune, n)
	for i := range s {
		s[i] = ' '
	}
	return s
}

// writeLineCells line を 1 rune ずつ書き出し、内部カーソルをセル単位で進める。
// 幅 w の rune を書く時点で行の残り桁数が w に満たない場合、残り桁を空白で
// 埋めて折返してから rune を書く。2 セル文字を行末で分割した際の挙動は端末が
// 保証しないため、分割を起こさないこの規則を描画とカーソル計算が共有する。
// エスケープ列は表示セルを消費しないため、queue はするが桁を進めない
// (cellPos / visualWidth と同じ走査規則)。
func (t *Terminal) writeLineCells(line []rune) {
	inEscapeSeq := false
	for _, r := range line {
		w := 0
		switch {
		case inEscapeSeq:
			if isEscapeTerminator(r) {
				inEscapeSeq = false
			}
		case r == '\x1b':
			inEscapeSeq = true
		default:
			w = runeCells(r)
		}
		if remaining := t.termWidth - t.cursorX; w > remaining {
			t.queue(padding(remaining))
			t.advanceCursor(remaining)
		}
		t.queueRune(r)
		t.advanceCursor(w)
	}
}

// eraseCells 旧行を新行で上書きしたあとに消し残るセル数を返す (setLine 用)。
func eraseCells(old, newLine []rune) int {
	n := visualWidth(old) - visualWidth(newLine)
	if n < 0 {
		return 0
	}
	return n
}

// removedCells line の pos から n rune を削除したときに空くセル数を返す
// (eraseNPreviousChars 用)。line を書き換える前に呼ぶ。
func removedCells(line []rune, pos, n int) int {
	if pos < 0 || n <= 0 || pos+n > len(line) {
		return 0
	}
	return visualWidth(line[pos : pos+n])
}

// tailCells pos 以降の残りが占めるセル数を返す (keyDeleteLine 用)。
func tailCells(line []rune, pos int) int {
	if pos < 0 {
		pos = 0
	}
	if pos > len(line) {
		return 0
	}
	return visualWidth(line[pos:])
}
