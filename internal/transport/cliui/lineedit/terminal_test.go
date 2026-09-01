// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// This file is a fork of golang.org/x/term v0.45.0 terminal_test.go,
// modified for the display-width aware fork in this package. Tests that
// cover removed upstream features (ReadPassword, AutoCompleteCallback,
// MakeRaw) are dropped; CJK cases are added.

package lineedit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

type MockTerminal struct {
	toSend       []byte
	bytesPerRead int
	received     []byte
}

func (c *MockTerminal) Read(data []byte) (n int, err error) {
	n = len(data)
	if n == 0 {
		return
	}
	if n > len(c.toSend) {
		n = len(c.toSend)
	}
	if n == 0 {
		return 0, io.EOF
	}
	if c.bytesPerRead > 0 && n > c.bytesPerRead {
		n = c.bytesPerRead
	}
	copy(data, c.toSend[:n])
	c.toSend = c.toSend[n:]
	return
}

func (c *MockTerminal) Write(data []byte) (n int, err error) {
	c.received = append(c.received, data...)
	return len(data), nil
}

func TestClose(t *testing.T) {
	c := &MockTerminal{}
	ss := NewTerminal(c, "> ")
	line, err := ss.ReadLine()
	if line != "" {
		t.Errorf("Expected empty line but got: %s", line)
	}
	if err != io.EOF {
		t.Errorf("Error should have been EOF but got: %s", err)
	}
}

var keyPressTests = []struct {
	in             string
	line           string
	err            error
	throwAwayLines int
}{
	{
		err: io.EOF,
	},
	{
		in:   "\r",
		line: "",
	},
	{
		in:   "foo\r",
		line: "foo",
	},
	{
		in:   "a\x1b[Cb\r", // right
		line: "ab",
	},
	{
		in:   "a\x1b[Db\r", // left
		line: "ba",
	},
	{
		in:   "a\006b\r", // ^F
		line: "ab",
	},
	{
		in:   "a\002b\r", // ^B
		line: "ba",
	},
	{
		in:   "a\177b\r", // backspace
		line: "b",
	},
	{
		in: "\x1b[A\r", // up
	},
	{
		in: "\x1b[B\r", // down
	},
	{
		in: "\016\r", // ^P
	},
	{
		in: "\014\r", // ^N
	},
	{
		in:   "line\x1b[A\x1b[B\r", // up then down
		line: "line",
	},
	{
		in:             "line1\rline2\x1b[A\r", // recall previous line.
		line:           "line1",
		throwAwayLines: 1,
	},
	{
		// recall two previous lines and append.
		in:             "line1\rline2\rline3\x1b[A\x1b[Axxx\r",
		line:           "line1xxx",
		throwAwayLines: 2,
	},
	{
		// Ctrl-A to move to beginning of line followed by ^K to kill
		// line.
		in:   "a b \001\013\r",
		line: "",
	},
	{
		// Ctrl-A to move to beginning of line, Ctrl-E to move to end,
		// finally ^K to kill nothing.
		in:   "a b \001\005\013\r",
		line: "a b ",
	},
	{
		in:   "\027\r",
		line: "",
	},
	{
		in:   "a\027\r",
		line: "",
	},
	{
		in:   "a \027\r",
		line: "",
	},
	{
		in:   "a b\027\r",
		line: "a ",
	},
	{
		in:   "a b \027\r",
		line: "a ",
	},
	{
		in:   "one two thr\x1b[D\027\r",
		line: "one two r",
	},
	{
		in:   "\013\r",
		line: "",
	},
	{
		in:   "a\013\r",
		line: "a",
	},
	{
		in:   "ab\x1b[D\013\r",
		line: "a",
	},
	{
		in:   "Ξεσκεπάζω\r",
		line: "Ξεσκεπάζω",
	},
	{
		in:             "£\r\x1b[A\177\r", // non-ASCII char, enter, up, backspace.
		line:           "",
		throwAwayLines: 1,
	},
	{
		in:             "£\r££\x1b[A\x1b[B\177\r", // non-ASCII char, enter, 2x non-ASCII, up, down, backspace, enter.
		line:           "£",
		throwAwayLines: 1,
	},
	{
		// Ctrl-D at the end of the line should be ignored.
		in:   "a\004\r",
		line: "a",
	},
	{
		// a, b, left, Ctrl-D should erase the b.
		in:   "ab\x1b[D\004\r",
		line: "a",
	},
	{
		// a, b, c, d, left, left, ^U should erase to the beginning of
		// the line.
		in:   "abcd\x1b[D\x1b[D\025\r",
		line: "cd",
	},
	{
		// Bracketed paste mode: control sequences should be returned
		// verbatim in paste mode.
		in:   "abc\x1b[200~de\177f\x1b[201~\177\r",
		line: "abcde\177",
	},
	{
		// CR in bracketed paste is captured as a newline, not a submit.
		in:   "abc\x1b[200~d\refg\x1b[201~h\r",
		line: "abcd\nefgh",
	},
	{
		// Newline in bracketed paste collapses to a placeholder and expands on Enter.
		in:   "abc\x1b[200~d\nefg\x1b[201~h\r",
		line: "abcd\nefgh",
	},
	{
		// A short pasted fragment is inserted literally.
		in:   "\x1b[200~a\x1b[201~\r",
		line: "a",
	},
	{
		// A short pasted fragment submitted with LF.
		in:   "\x1b[200~a\x1b[201~\n",
		line: "a",
	},
	{
		// Ctrl-C terminates readline
		in:  "\003",
		err: io.EOF,
	},
	{
		// Ctrl-C at the end of line also terminates readline
		in:  "a\003\r",
		err: io.EOF,
	},
	{
		// Delete at EOL: nothing
		in:   "abc\x1b[3~\r",
		line: "abc",
	},
	{
		// Delete in empty string: nothing
		in:   "\x1b[3~\r",
		line: "",
	},
	{
		// Move left, delete: delete 'c'
		in:   "abc\x1b[D\x1b[3~\r",
		line: "ab",
	},
	{
		// Home, delete: delete 'a'
		in:   "abc\x1b[H\x1b[3~\r",
		line: "bc",
	},
	{
		// Home, delete twice: delete 'a' and 'b'
		in:   "abc\x1b[H\x1b[3~\x1b[3~\r",
		line: "c",
	},
	{
		// Ctrl-T at end of line: transpose last two chars
		in:   "abc\x14\r",
		line: "acb",
	},
	{
		// Ctrl-T at end then type: cursor stays at end
		in:   "abc\x14N\r",
		line: "acbN",
	},
	{
		// Ctrl-T in middle: transpose chars before cursor, move cursor forward
		in:   "abc\x1b[D\x14\r",
		line: "acb",
	},
	{
		// Ctrl-T in middle then type: cursor moved past swapped char
		in:   "abcd\x1b[D\x1b[D\x14N\r",
		line: "acbNd",
	},
	{
		// Ctrl-T at pos 1 then type: cursor moves to pos 2
		in:   "abc\x1b[H\x1b[C\x14N\r",
		line: "baNc",
	},
	{
		// Ctrl-T with one char: do nothing
		in:   "a\x14\r",
		line: "a",
	},
	{
		// Ctrl-T with one char then type: cursor unchanged
		in:   "a\x14N\r",
		line: "aN",
	},
	{
		// Ctrl-T at beginning: do nothing
		in:   "ab\x1b[H\x14\r",
		line: "ab",
	},
	{
		// Ctrl-T at beginning then type: cursor unchanged, inserts at start
		in:   "ab\x1b[H\x14N\r",
		line: "Nab",
	},
	{
		// Ctrl-T on empty line: do nothing
		in:   "\x14\r",
		line: "",
	},
	{
		// Multiple Ctrl-T at end: keeps swapping last two
		in:   "abc\x14\x14\r",
		line: "abc",
	},
	{
		// Multiple Ctrl-T in middle: progresses through line
		in:   "abcd\x1b[D\x1b[D\x1b[D\x14\x14\x14\r",
		line: "bcda",
	},
	{
		// Alt-Left で単語頭へ、Alt-Right で単語末へ移動する
		in:   "one two\x1b[1;3D\x1b[1;3Cx\r",
		line: "one twox",
	},
	{
		// CJK を含む行での Alt-Left
		in:   "日本 語\x1b[1;3Dx\r",
		line: "日本 x語",
	},
	{
		// CJK 入力がそのまま確定する
		in:   "日本語\r",
		line: "日本語",
	},
	{
		// CJK の Backspace は 1 文字だけ消す
		in:   "日本語\177\r",
		line: "日本",
	},
	{
		// 左矢印で 1 文字戻ってから Backspace
		in:   "日本語\x1b[D\177\r",
		line: "日語",
	},
	{
		// Home から Ctrl-K で CJK 行を全消去
		in:   "日本語\001\013\r",
		line: "",
	},
	{
		// 左矢印 1 回のあと Ctrl-U で手前を消す
		in:   "日本語\x1b[D\025\r",
		line: "語",
	},
	{
		// CJK と ASCII の混在で Delete キー
		in:   "a日b\x1b[H\x1b[3~\r",
		line: "日b",
	},
	{
		// CJK 行の履歴呼出し
		in:             "日本語のテスト\rls\x1b[A\r",
		line:           "日本語のテスト",
		throwAwayLines: 1,
	},
	{
		// bracketed paste 中の CJK
		in:   "\x1b[200~日本語\x1b[201~\r",
		line: "日本語",
	},
}

func TestKeyPresses(t *testing.T) {
	for i, test := range keyPressTests {
		for j := 1; j < len(test.in); j++ {
			c := &MockTerminal{
				toSend:       []byte(test.in),
				bytesPerRead: j,
			}
			ss := NewTerminal(c, "> ")
			for k := 0; k < test.throwAwayLines; k++ {
				_, err := ss.ReadLine()
				if err != nil {
					t.Errorf("Throwaway line %d from test %d resulted in error: %s", k, i, err)
				}
			}
			line, err := ss.ReadLine()
			if line != test.line {
				t.Errorf("Line resulting from test %d (%d bytes per read) was '%s', expected '%s'", i, j, line, test.line)
				break
			}
			if err != test.err {
				t.Errorf("Error resulting from test %d (%d bytes per read) was '%v', expected '%v'", i, j, err, test.err)
				break
			}
		}
	}
}

var renderTests = []struct {
	in       string
	received string
	err      error
}{
	{
		// Cursor move after keyHome (left 4) then enter (right 4, newline)
		in:       "abcd\x1b[H\r",
		received: "> abcd\x1b[4D\x1b[4C\r\n",
	},
	{
		// Write, home, prepend, enter. Prepends rewrites the line.
		in: "cdef\x1b[Hab\r",
		received: "> cdef" + // Initial input
			"\x1b[4Da" + // Move cursor back, insert first char
			"cdef" + // Copy over original string
			"\x1b[4Dbcdef" + // Repeat for second char with copy
			"\x1b[4D" + // Put cursor back in position to insert again
			"\x1b[4C\r\n", // Put cursor at the end of the line and newline.
	},
}

func TestRender(t *testing.T) {
	for i, test := range renderTests {
		for j := 1; j < len(test.in); j++ {
			c := &MockTerminal{
				toSend:       []byte(test.in),
				bytesPerRead: j,
			}
			ss := NewTerminal(c, "> ")
			_, err := ss.ReadLine()
			if err != test.err {
				t.Errorf("Error resulting from test %d (%d bytes per read) was '%v', expected '%v'", i, j, err, test.err)
				break
			}
			if test.received != string(c.received) {
				t.Errorf("Results rendered from test %d (%d bytes per read) was '%s', expected '%s'", i, j, c.received, test.received)
				break
			}
		}
	}
}

func TestCRLF(t *testing.T) {
	c := &MockTerminal{
		toSend: []byte("line1\rline2\r\nline3\n"),
		// bytesPerRead 0 in this test means read all at once
		// CR+LF need to be in same read for ReadLine to not produce an extra empty line
		// which is what terminals do for reasonably small paste. if way many lines are pasted
		// and going over say 1k-16k buffer, readline current implementation will possibly generate 1
		// extra empty line, if the CR is in chunk1 and LF in chunk2 (and that's fine).
	}

	ss := NewTerminal(c, "> ")
	for i := range 3 {
		line, err := ss.ReadLine()
		if err != nil {
			t.Fatalf("failed to read line %d: %v", i+1, err)
		}
		expected := fmt.Sprintf("line%d", i+1)
		if line != expected {
			t.Fatalf("expected '%s', got '%s'", expected, line)
		}
	}
	line, err := ss.ReadLine()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after 3 lines, got '%s' with error %v", line, err)
	}
	if line != "" {
		t.Fatalf("expected empty line after EOF, got '%s'", line)
	}
}

var setSizeTests = []struct {
	width, height int
}{
	{40, 13},
	{80, 24},
	{132, 43},
}

func TestTerminalSetSize(t *testing.T) {
	for _, setSize := range setSizeTests {
		c := &MockTerminal{
			toSend:       []byte("hello\r"),
			bytesPerRead: 1,
		}
		ss := NewTerminal(c, "> ")
		if err := ss.SetSize(setSize.width, setSize.height); err != nil {
			t.Fatalf("SetSize: %v", err)
		}
		line, err := ss.ReadLine()
		if err != nil {
			t.Fatalf("failed to read line: %v", err)
		}
		if line != "hello" {
			t.Fatalf("failed to read line, got %s", line)
		}
	}
}

func TestOutputNewlines(t *testing.T) {
	// \n should be changed to \r\n in terminal output.
	buf := new(bytes.Buffer)
	term := NewTerminal(buf, ">")

	term.Write([]byte("1\n2\n"))
	output := buf.String()
	const expected = "1\r\n2\r\n"

	if output != expected {
		t.Errorf("incorrect output: was %q, expected %q", output, expected)
	}
}

// --- 以下は本フォークで追加した CJK 表示幅の回帰テスト ---

// runCJK は入力を 1 回の ReadLine へ流し、Terminal と出力バイト列を返す。
func runCJK(input string, width int, hist History) (*Terminal, string, string, error) {
	mock := &MockTerminal{toSend: []byte(input), bytesPerRead: 1}
	term := NewTerminal(mock, "> ")
	if hist != nil {
		term.History = hist
	}
	if width > 0 {
		if err := term.SetSize(width, 24); err != nil {
			panic(err)
		}
	}
	line, err := term.ReadLine()
	return term, line, string(mock.received), err
}

func TestCursorMoveIsCellBased(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "left over cjk moves 2 cells", input: "日本語\x1b[D", want: "\x1b[2D"},
		{name: "left over ascii moves 1 cell", input: "abc\x1b[D", want: "\x1b[D"},
		{name: "home over cjk moves 6 cells", input: "日本語\x01", want: "\x1b[6D"},
		{name: "home over mixed moves 4 cells", input: "a日b\x01", want: "\x1b[4D"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, received, _ := runCJK(tt.input, 0, nil)
			if !strings.Contains(received, tt.want) {
				t.Errorf("received %q does not contain %q", received, tt.want)
			}
		})
	}
}

func TestBackspaceErasesCellWidth(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string // 残った行の再描画に続く消去空白
	}{
		{name: "cjk erases two cells", input: "日本語\x1b[D\x7f", want: "語  "},
		{name: "ascii erases one cell", input: "abc\x1b[D\x7f", want: "c "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, received, _ := runCJK(tt.input, 0, nil)
			if !strings.Contains(received, tt.want) {
				t.Errorf("received %q does not contain %q", received, tt.want)
			}
		})
	}
}

// 幅 21、プロンプト "> " (2 セル)。CJK 9 文字で 20 セルまで埋まるため、
// 10 文字目は残り 1 桁に収まらず、空白 1 個のパディングと折返しが入る。
func TestWrapPadsBeforeWideRune(t *testing.T) {
	term, _, received, _ := runCJK("日本語日本語日本語日", 21, nil)
	if !strings.Contains(received, " \r\n") {
		t.Errorf("received %q does not contain the padding + wrap", received)
	}
	wantX, wantY := cellPos([]rune("> "), []rune("日本語日本語日本語日"), 21)
	if term.cursorX != wantX || term.cursorY != wantY {
		t.Errorf("cursor = (%d,%d), want (%d,%d)", term.cursorX, term.cursorY, wantX, wantY)
	}
}

func TestHistoryRecallErasesCellRemainder(t *testing.T) {
	hist := &stubHistory{entries: []string{"ls"}}
	// 長い CJK 行を入力してから上矢印で短い ASCII 行を呼び出す。
	term, _, received, _ := runCJK("日本語のテスト\x1b[A", 0, hist)
	if string(term.line) != "ls" {
		t.Fatalf("line = %q, want %q", string(term.line), "ls")
	}
	// 14 セルから 2 セルへ縮むので 12 セル分の空白で消す。
	if !strings.Contains(received, "ls"+strings.Repeat(" ", 12)) {
		t.Errorf("received %q does not erase the 12 remaining cells", received)
	}
}

func TestDeleteToEndErasesCellWidth(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // 消去に使う空白セル数
	}{
		{name: "ctrl-k over cjk", input: "日本語\x01\x0b", want: 6},
		{name: "ctrl-k over mixed", input: "a日\x01\x0b", want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, received, _ := runCJK(tt.input, 0, nil)
			if !strings.Contains(received, strings.Repeat(" ", tt.want)) {
				t.Errorf("received %q does not contain %d erase cells", received, tt.want)
			}
			if strings.Contains(received, strings.Repeat(" ", tt.want+1)) {
				t.Errorf("received %q erases more than %d cells", received, tt.want)
			}
		})
	}
}

func TestCtrlUErasesCellWidth(t *testing.T) {
	_, _, received, _ := runCJK("日本語\x1b[D\x15", 0, nil)
	// 手前 2 文字 (4 セル) を消す。
	if !strings.Contains(received, "語"+strings.Repeat(" ", 4)) {
		t.Errorf("received %q does not erase 4 cells", received)
	}
}

func TestPastedCJKKeepsCellPosition(t *testing.T) {
	term, line, _, err := runCJK("\x1b[200~日本語\x1b[201~\r", 0, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if line != "日本語" {
		t.Fatalf("line = %q", line)
	}
	// Enter で行がリセットされるため、次の行の先頭に戻っている。
	if term.cursorX != 0 || term.cursorY != 0 {
		t.Errorf("cursor = (%d,%d), want (0,0)", term.cursorX, term.cursorY)
	}
}

// 読み取りエラーは ReadLine からそのまま返る。
func TestReadErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom")
	term := NewTerminal(&errReader{err: wantErr}, "> ")
	line, err := term.ReadLine()
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if line != "" {
		t.Errorf("line = %q, want empty", line)
	}
}

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error)    { return 0, r.err }
func (r *errReader) Write(p []byte) (int, error) { return len(p), nil }

func TestClearScreenUsesPromptCellWidth(t *testing.T) {
	term, _, received, _ := runCJK("日本\x0c", 0, nil)
	if !strings.Contains(received, "\x1b[2J\x1b[H> 日本") {
		t.Errorf("received %q does not repaint the prompt and line", received)
	}
	wantX, wantY := cellPos([]rune("> "), []rune("日本"), term.termWidth)
	if term.cursorX != wantX || term.cursorY != wantY {
		t.Errorf("cursor = (%d,%d), want (%d,%d)", term.cursorX, term.cursorY, wantX, wantY)
	}
}

// Write は行編集中の画面を消してから出力し、プロンプトと行を描き直す。
func TestWriteRepaintsCJKLine(t *testing.T) {
	mock := &MockTerminal{toSend: []byte("日本"), bytesPerRead: 1}
	term := NewTerminal(mock, "> ")
	if _, err := term.ReadLine(); !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	mock.received = nil
	if _, err := term.Write([]byte("output\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	received := string(mock.received)
	if !strings.Contains(received, "output\r\n") {
		t.Errorf("received %q does not contain the CRLF converted output", received)
	}
	if !strings.Contains(received, "> 日本") {
		t.Errorf("received %q does not repaint prompt and line", received)
	}
	wantX, wantY := cellPos([]rune("> "), []rune("日本"), term.termWidth)
	if term.cursorX != wantX || term.cursorY != wantY {
		t.Errorf("cursor = (%d,%d), want (%d,%d)", term.cursorX, term.cursorY, wantX, wantY)
	}
}

// Write は画面に何も無ければ変換だけ行う。
func TestWriteWithoutPrompt(t *testing.T) {
	buf := new(bytes.Buffer)
	term := NewTerminal(struct {
		io.Reader
		io.Writer
	}{bytes.NewReader(nil), buf}, "> ")
	if _, err := term.Write([]byte("1\n2\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if buf.String() != "1\r\n2\r\n" {
		t.Errorf("output = %q", buf.String())
	}
}

// SetSize は再描画後もセル基準で内部座標を保つ。
func TestSetSizeRepaintsCJKLine(t *testing.T) {
	tests := []struct {
		name     string
		width    int
		wantSame bool
	}{
		{name: "shrink", width: 12},
		{name: "grow", width: 60},
		{name: "unchanged", width: 40, wantSame: true},
		{name: "zero clamps to one", width: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockTerminal{toSend: []byte("日本語のテスト"), bytesPerRead: 1}
			term := NewTerminal(mock, "> ")
			if err := term.SetSize(40, 24); err != nil {
				t.Fatalf("SetSize: %v", err)
			}
			if _, err := term.ReadLine(); !errors.Is(err, io.EOF) {
				t.Fatalf("err = %v, want EOF", err)
			}
			if err := term.SetSize(tt.width, 24); err != nil {
				t.Fatalf("SetSize: %v", err)
			}
			want := tt.width
			if want == 0 {
				want = 1
			}
			if term.termWidth != want {
				t.Errorf("termWidth = %d, want %d", term.termWidth, want)
			}
			if tt.wantSame {
				return
			}
			if term.cursorX >= term.termWidth {
				t.Errorf("cursorX = %d, must be inside width %d", term.cursorX, term.termWidth)
			}
		})
	}
}

// 何も表示していない状態の SetSize は再描画しない。
func TestSetSizeOnEmptyScreen(t *testing.T) {
	mock := &MockTerminal{}
	term := NewTerminal(mock, "> ")
	if err := term.SetSize(30, 10); err != nil {
		t.Fatalf("SetSize: %v", err)
	}
	if len(mock.received) != 0 {
		t.Errorf("received %q, want no output", string(mock.received))
	}
}
