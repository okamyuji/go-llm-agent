package lineedit

import "testing"

func TestRuneCells(t *testing.T) {
	tests := []struct {
		name string
		in   rune
		want int
	}{
		{name: "ascii", in: 'a', want: 1},
		{name: "digit", in: '0', want: 1},
		{name: "cjk", in: '日', want: 2},
		{name: "hiragana", in: 'あ', want: 2},
		{name: "fullwidth latin", in: 'Ａ', want: 2},
		{name: "halfwidth kana", in: 'ｱ', want: 1},
		{name: "control nul", in: '\x00', want: 0},
		{name: "control esc", in: '\x1b', want: 0},
		{name: "combining acute", in: '́', want: 0},
		{name: "emoji", in: '😀', want: 2},
		{name: "greek", in: 'Ξ', want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runeCells(tt.in); got != tt.want {
				t.Errorf("runeCells(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestVisualWidth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{name: "empty", in: "", want: 0},
		{name: "ascii only", in: "abc", want: 3},
		{name: "cjk only", in: "日本語", want: 6},
		{name: "mixed", in: "a日b", want: 4},
		{name: "escape sequence stripped", in: "\x1b[31mA", want: 1},
		{name: "escape sequence only", in: "\x1b[0m", want: 0},
		{name: "escape before cjk", in: "\x1b[31m日", want: 2},
		{name: "control char", in: "a\x00b", want: 2},
		{name: "combining", in: "é", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := visualWidth([]rune(tt.in)); got != tt.want {
				t.Errorf("visualWidth(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestPromptCells(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain", in: ">> ", want: ">> "},
		{name: "leading escape", in: "\x1b[31m>> ", want: ">> "},
		{name: "trailing escape", in: ">> \x1b[0m", want: ">> "},
		{name: "escape in middle", in: ">\x1b[1m>\x1b[0m ", want: ">> "},
		{name: "cjk kept", in: "日> ", want: "日> "},
		{name: "escape with digits only ends at letter", in: "\x1b[38;5;1mX", want: "X"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(promptCells([]rune(tt.in))); got != tt.want {
				t.Errorf("promptCells(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// promptCells は常に新規スライスを返す契約を持つ。呼び出し側 (cellPos) が結果へ
// append しても入力の backing array を壊さないことがこの契約の目的である。
func TestPromptCellsDoesNotAliasInput(t *testing.T) {
	in := []rune("\x1b[31m>> ")
	got := promptCells(in)
	if len(got) == cap(got) {
		t.Fatalf("test precondition: want spare capacity, len=%d cap=%d", len(got), cap(got))
	}
	got = append(got, 'X')
	if string(in) != "\x1b[31m>> " {
		t.Errorf("input mutated by append to result: %q", string(in))
	}
	if got[len(got)-1] != 'X' {
		t.Errorf("append lost: %q", string(got))
	}
}

func TestAdvanceCell(t *testing.T) {
	tests := []struct {
		name           string
		x, y, w, width int
		wantX, wantY   int
	}{
		{name: "ascii mid line", x: 0, y: 0, w: 1, width: 10, wantX: 1, wantY: 0},
		{name: "cjk mid line", x: 0, y: 0, w: 2, width: 10, wantX: 2, wantY: 0},
		{name: "zero width", x: 3, y: 1, w: 0, width: 10, wantX: 3, wantY: 1},
		{name: "ascii exactly at line end", x: 9, y: 0, w: 1, width: 10, wantX: 0, wantY: 1},
		{name: "cjk exactly at line end", x: 8, y: 0, w: 2, width: 10, wantX: 0, wantY: 1},
		{name: "cjk does not fit in last cell", x: 9, y: 0, w: 2, width: 10, wantX: 2, wantY: 1},
		{name: "ascii on width 1", x: 0, y: 0, w: 1, width: 1, wantX: 0, wantY: 1},
		{name: "cjk on width 1", x: 0, y: 0, w: 2, width: 1, wantX: 0, wantY: 3},
		{name: "zero width guarded to 1", x: 0, y: 0, w: 1, width: 0, wantX: 0, wantY: 1},
		{name: "negative width guarded to 1", x: 0, y: 0, w: 1, width: -5, wantX: 0, wantY: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, y := advanceCell(tt.x, tt.y, tt.w, tt.width)
			if x != tt.wantX || y != tt.wantY {
				t.Errorf("advanceCell(%d,%d,%d,%d) = (%d,%d), want (%d,%d)",
					tt.x, tt.y, tt.w, tt.width, x, y, tt.wantX, tt.wantY)
			}
		})
	}
}

func TestCellPos(t *testing.T) {
	tests := []struct {
		name         string
		prompt, text string
		width        int
		wantX, wantY int
	}{
		{name: "empty everything", prompt: "", text: "", width: 80, wantX: 0, wantY: 0},
		{name: "prompt only", prompt: ">> ", text: "", width: 80, wantX: 3, wantY: 0},
		{name: "ascii text", prompt: ">> ", text: "abc", width: 80, wantX: 6, wantY: 0},
		{name: "cjk text", prompt: ">> ", text: "日本語", width: 80, wantX: 9, wantY: 0},
		{name: "escape in prompt ignored", prompt: "\x1b[31m>> ", text: "日", width: 80, wantX: 5, wantY: 0},
		{name: "wrap on ascii", prompt: ">> ", text: "abcdefg", width: 10, wantX: 0, wantY: 1},
		// 幅 20、プロンプト 3 セル。CJK 8 文字で 19 セル。9 文字目は残り 1 桁に
		// 収まらないため空白 1 個で折返してから書かれ、次行の 2 桁目に着地する。
		{name: "cjk padding wrap", prompt: ">> ", text: "日本語日本語日本語", width: 20, wantX: 2, wantY: 1},
		{name: "cjk exactly fills line", prompt: "", text: "日本語日本語日本語日", width: 20, wantX: 0, wantY: 1},
		// 幅 1 の端末。2 セル文字は残り 1 桁に収まらないので折返してから書かれ、
		// 書いた 2 セル分がさらに 2 行送られる (advanceCursor と同じ除算・剰余)。
		{name: "width one with cjk", prompt: "", text: "日", width: 1, wantX: 0, wantY: 3},
		{name: "control chars have no effect", prompt: "", text: "a\x00b", width: 80, wantX: 2, wantY: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, y := cellPos([]rune(tt.prompt), []rune(tt.text), tt.width)
			if x != tt.wantX || y != tt.wantY {
				t.Errorf("cellPos(%q,%q,%d) = (%d,%d), want (%d,%d)",
					tt.prompt, tt.text, tt.width, x, y, tt.wantX, tt.wantY)
			}
		})
	}
}
