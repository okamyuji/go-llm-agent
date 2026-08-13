package lineedit

import (
	"strings"
	"testing"
)

func TestPadding(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want string
	}{
		{name: "zero", n: 0, want: ""},
		{name: "negative", n: -3, want: ""},
		{name: "one", n: 1, want: " "},
		{name: "many", n: 4, want: "    "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(padding(tt.n)); got != tt.want {
				t.Errorf("padding(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

func TestEraseCells(t *testing.T) {
	tests := []struct {
		name         string
		old, newLine string
		want         int
	}{
		{name: "both empty", old: "", newLine: "", want: 0},
		{name: "ascii shrink", old: "abcd", newLine: "ab", want: 2},
		{name: "cjk to short ascii", old: "日本語のテスト", newLine: "ls", want: 12},
		{name: "grow returns zero", old: "ab", newLine: "日本語", want: 0},
		{name: "same width", old: "日", newLine: "ab", want: 0},
		{name: "cleared entirely", old: "日本", newLine: "", want: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eraseCells([]rune(tt.old), []rune(tt.newLine)); got != tt.want {
				t.Errorf("eraseCells(%q,%q) = %d, want %d", tt.old, tt.newLine, got, tt.want)
			}
		})
	}
}

func TestRemovedCells(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		pos, n int
		want   int
	}{
		{name: "one ascii", line: "abc", pos: 1, n: 1, want: 1},
		{name: "one cjk", line: "a日c", pos: 1, n: 1, want: 2},
		{name: "multiple mixed", line: "a日本c", pos: 1, n: 2, want: 4},
		{name: "whole line", line: "日本", pos: 0, n: 2, want: 4},
		{name: "n zero", line: "abc", pos: 0, n: 0, want: 0},
		{name: "n negative", line: "abc", pos: 0, n: -1, want: 0},
		{name: "pos negative", line: "abc", pos: -1, n: 1, want: 0},
		{name: "out of range", line: "abc", pos: 2, n: 5, want: 0},
		{name: "empty line", line: "", pos: 0, n: 1, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := removedCells([]rune(tt.line), tt.pos, tt.n); got != tt.want {
				t.Errorf("removedCells(%q,%d,%d) = %d, want %d", tt.line, tt.pos, tt.n, got, tt.want)
			}
		})
	}
}

func TestTailCells(t *testing.T) {
	tests := []struct {
		name string
		line string
		pos  int
		want int
	}{
		{name: "from start ascii", line: "abc", pos: 0, want: 3},
		{name: "from start cjk", line: "日本", pos: 0, want: 4},
		{name: "middle", line: "a日本", pos: 1, want: 4},
		{name: "at end", line: "abc", pos: 3, want: 0},
		{name: "empty line", line: "", pos: 0, want: 0},
		{name: "pos negative clamped", line: "ab", pos: -2, want: 2},
		{name: "pos beyond end", line: "ab", pos: 5, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tailCells([]rune(tt.line), tt.pos); got != tt.want {
				t.Errorf("tailCells(%q,%d) = %d, want %d", tt.line, tt.pos, got, tt.want)
			}
		})
	}
}

func TestPromptCellWidth(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   int
	}{
		{name: "ascii prompt", prompt: ">> ", want: 3},
		{name: "cjk prompt", prompt: "入力> ", want: 6},
		{name: "escape in prompt", prompt: "\x1b[31m>> ", want: 3},
		{name: "empty prompt", prompt: "", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := NewTerminal(&MockTerminal{}, tt.prompt)
			if got := term.promptCellWidth(); got != tt.want {
				t.Errorf("promptCellWidth(%q) = %d, want %d", tt.prompt, got, tt.want)
			}
		})
	}
}

// 検索中は表示プロンプトが検索プロンプトへ切り替わるため、幅もそちらを返す。
func TestPromptCellWidthDuringSearch(t *testing.T) {
	term := NewTerminal(&MockTerminal{}, ">> ")
	term.search.active = true
	term.search.query = []rune("本語")
	want := visualWidth([]rune("(reverse-i-search)'本語': "))
	if got := term.promptCellWidth(); got != want {
		t.Errorf("promptCellWidth during search = %d, want %d", got, want)
	}
}

func TestWriteLineCells(t *testing.T) {
	tests := []struct {
		name         string
		width        int
		prompt       string
		line         string
		wantOut      string
		wantX, wantY int
	}{
		{
			name: "ascii fits", width: 20, prompt: "", line: "abc",
			wantOut: "abc", wantX: 3, wantY: 0,
		},
		{
			name: "cjk fits", width: 20, prompt: "", line: "日本",
			wantOut: "日本", wantX: 4, wantY: 0,
		},
		{
			name: "empty line writes nothing", width: 20, prompt: "", line: "",
			wantOut: "", wantX: 0, wantY: 0,
		},
		{
			// 幅 5 に ASCII を 5 文字。行末ちょうどで advanceCursor が \r\n を補う。
			name: "ascii exactly fills line", width: 5, prompt: "", line: "abcde",
			wantOut: "abcde\r\n", wantX: 0, wantY: 1,
		},
		{
			// 幅 5 に 2 セル文字 3 個。残り 1 桁に 3 個目が収まらないため
			// 空白 1 個で折返してから書く。
			name: "cjk padded wrap", width: 5, prompt: "", line: "日本語",
			wantOut: "日本 \r\n語", wantX: 2, wantY: 1,
		},
		{
			name: "zero width rune consumes no cell", width: 20, prompt: "", line: "a\x00b",
			wantOut: "a\x00b", wantX: 2, wantY: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &MockTerminal{}
			term := NewTerminal(mock, tt.prompt)
			term.termWidth = tt.width
			term.writeLineCells([]rune(tt.line))
			if got := string(term.outBuf); got != tt.wantOut {
				t.Errorf("outBuf = %q, want %q", got, tt.wantOut)
			}
			if term.cursorX != tt.wantX || term.cursorY != tt.wantY {
				t.Errorf("cursor = (%d,%d), want (%d,%d)", term.cursorX, term.cursorY, tt.wantX, tt.wantY)
			}
		})
	}
}

// 描画側 (writeLineCells) の内部カーソルと、カーソル計算側 (cellPos) が
// 常に一致することを総当たりで確認する。3.3.2 の共有規則の中核。
func TestWriteLineCellsMatchesCellPos(t *testing.T) {
	prompts := []string{"", ">> ", "入力> ", "\x1b[31m>> "}
	sources := []rune("あiう本eか日gく語")
	for _, prompt := range prompts {
		for width := 1; width <= 40; width++ {
			for length := 0; length <= 60; length++ {
				line := make([]rune, 0, length)
				for i := 0; i < length; i++ {
					line = append(line, sources[i%len(sources)])
				}
				term := NewTerminal(&MockTerminal{}, prompt)
				term.termWidth = width
				term.writeLineCells(term.displayPrompt())
				term.writeLineCells(line)
				wantX, wantY := cellPos([]rune(prompt), line, width)
				if term.cursorX != wantX || term.cursorY != wantY {
					t.Fatalf("prompt=%q width=%d length=%d: writeLineCells cursor=(%d,%d), cellPos=(%d,%d)",
						prompt, width, length, term.cursorX, term.cursorY, wantX, wantY)
				}
			}
		}
	}
}

// 3.3.2 の折返し規則は、印字したセルが行幅を超えないことを保証する。
func TestWriteLineCellsNeverExceedsWidth(t *testing.T) {
	for width := 2; width <= 8; width++ {
		term := NewTerminal(&MockTerminal{}, "")
		term.termWidth = width
		term.writeLineCells([]rune("日本語abc日本"))
		for _, row := range strings.Split(string(term.outBuf), "\r\n") {
			if w := visualWidth([]rune(row)); w > width {
				t.Errorf("width=%d: row %q has %d cells", width, row, w)
			}
		}
	}
}
