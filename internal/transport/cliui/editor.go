package cliui

import (
	"errors"
	"io"
	"strconv"
)

// errInterrupted は行編集中に Ctrl-C が押されたことを表す。
var errInterrupted = errors.New("interrupted")

// lineEditor は raw モード前提の 1 行入力エディタ。
// 入力はキーストリーム、出力は端末。日本語 (East Asian Wide) を桁ずれなく描画し、
// カーソル移動・Backspace・↑↓履歴を扱う。cooked モードの bufio.Scanner を置き換える。
type lineEditor struct {
	in      keySource
	out     io.Writer
	prompt  string
	history []string
}

func newLineEditor(rd io.Reader, out io.Writer, prompt string) *lineEditor {
	return &lineEditor{in: newKeyReader(rd), out: out, prompt: prompt}
}

// newLineEditorFromSource は共有の keySource からエディタを作る (REPL の 1 本化入力用)。
func newLineEditorFromSource(src keySource, out io.Writer, prompt string) *lineEditor {
	return &lineEditor{in: src, out: out, prompt: prompt}
}

// readLine は 1 行を読み取って返す。
// Enter で確定、Ctrl-C で errInterrupted、空バッファでの Ctrl-D / ストリーム終端で io.EOF。
func (e *lineEditor) readLine() (string, error) {
	buf := []rune{}
	cursor := 0
	// histIdx == len(history) は「編集中の下書き」を指す
	histIdx := len(e.history)
	draft := []rune{}

	e.render(buf, cursor)
	for {
		ev, ok := e.in.next()
		if !ok {
			if len(buf) == 0 {
				return "", io.EOF
			}
			// 入力途中でストリームが尽きた場合は、それまでの内容を確定する
			return string(buf), nil
		}
		switch ev.kind {
		case keyRune:
			buf = append(buf[:cursor], append([]rune{ev.r}, buf[cursor:]...)...)
			cursor++
		case keyEnter:
			e.write("\r\n")
			line := string(buf)
			e.pushHistory(line)
			return line, nil
		case keyBackspace:
			if cursor > 0 {
				buf = append(buf[:cursor-1], buf[cursor:]...)
				cursor--
			}
		case keyLeft:
			if cursor > 0 {
				cursor--
			}
		case keyRight:
			if cursor < len(buf) {
				cursor++
			}
		case keyUp:
			if histIdx == len(e.history) {
				draft = append([]rune{}, buf...) // 下書きを退避
			}
			if histIdx > 0 {
				histIdx--
				buf = []rune(e.history[histIdx])
				cursor = len(buf)
			}
		case keyDown:
			if histIdx < len(e.history) {
				histIdx++
				if histIdx == len(e.history) {
					buf = append([]rune{}, draft...) // 下書きを復元
				} else {
					buf = []rune(e.history[histIdx])
				}
				cursor = len(buf)
			}
		case keyCtrlC:
			e.write("\r\n")
			return "", errInterrupted
		case keyEOF:
			if len(buf) == 0 {
				return "", io.EOF
			}
			// 非空ならバッファを確定 (端末の Ctrl-D 慣習)
			e.write("\r\n")
			line := string(buf)
			e.pushHistory(line)
			return line, nil
		default:
			// keyEsc / keyUnknown は行編集中は無視する
			continue
		}
		e.render(buf, cursor)
	}
}

// pushHistory は空でなく直前と異なる行だけ履歴に積む。
func (e *lineEditor) pushHistory(line string) {
	if line == "" {
		return
	}
	if n := len(e.history); n > 0 && e.history[n-1] == line {
		return
	}
	e.history = append(e.history, line)
}

// render は行頭からプロンプトとバッファを描き直し、カーソルを正しい桁へ置く。
// East Asian Wide を考慮して後方桁数ぶんカーソルを左に戻す。
func (e *lineEditor) render(buf []rune, cursor int) {
	// 行頭へ戻り行末まで消去 → プロンプト + バッファを描画
	e.write("\r\x1b[K")
	e.write(e.prompt)
	e.write(string(buf))
	back := runesWidth(buf[cursor:])
	if back > 0 {
		e.write("\x1b[" + strconv.Itoa(back) + "D")
	}
}

func (e *lineEditor) write(s string) {
	_, _ = io.WriteString(e.out, s)
}
