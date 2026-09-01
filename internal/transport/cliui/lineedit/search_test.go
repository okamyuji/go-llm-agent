package lineedit

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// stubHistory は固定の履歴を返す History 実装。index 0 が最新。
type stubHistory struct {
	entries []string
	added   []string
}

func (h *stubHistory) Add(entry string) { h.added = append(h.added, entry) }
func (h *stubHistory) Len() int         { return len(h.entries) }
func (h *stubHistory) At(idx int) string {
	return h.entries[idx]
}

func newStubHistory() *stubHistory {
	return &stubHistory{entries: []string{
		"ls -la",
		"日本語のテスト二",
		"grep 日本語",
		"日本語のテスト",
	}}
}

// runSearch は入力バイト列を 1 回の ReadLine へ流し、終了後の Terminal を返す。
// 入力が確定行を含まない場合、ReadLine は EOF で戻り内部状態を観測できる。
func runSearch(input string, hist History) (*Terminal, string, string, error) {
	mock := &MockTerminal{toSend: []byte(input), bytesPerRead: 1}
	term := NewTerminal(mock, ">> ")
	term.History = hist
	line, err := term.ReadLine()
	return term, line, string(mock.received), err
}

func TestDisplayPrompt(t *testing.T) {
	tests := []struct {
		name   string
		active bool
		failed bool
		query  string
		want   string
	}{
		{name: "not searching", active: false, want: ">> "},
		{name: "empty query", active: true, want: "(reverse-i-search)'': "},
		{name: "ascii query", active: true, query: "ls", want: "(reverse-i-search)'ls': "},
		{name: "cjk query", active: true, query: "本語", want: "(reverse-i-search)'本語': "},
		{name: "failed", active: true, failed: true, query: "zz", want: "(failed reverse-i-search)'zz': "},
		{name: "failed but not active", active: false, failed: true, query: "zz", want: ">> "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := NewTerminal(&MockTerminal{}, ">> ")
			term.search.active = tt.active
			term.search.failed = tt.failed
			term.search.query = []rune(tt.query)
			if got := string(term.displayPrompt()); got != tt.want {
				t.Errorf("displayPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSearchBackward(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		start     int
		wantIdx   int
		wantEntry string
		wantOK    bool
	}{
		{name: "ascii match", query: "ls", start: 0, wantIdx: 0, wantEntry: "ls -la", wantOK: true},
		{name: "cjk substring", query: "本語", start: 0, wantIdx: 1, wantEntry: "日本語のテスト二", wantOK: true},
		{name: "skips to older", query: "本語", start: 2, wantIdx: 2, wantEntry: "grep 日本語", wantOK: true},
		{name: "last match", query: "本語", start: 3, wantIdx: 3, wantEntry: "日本語のテスト", wantOK: true},
		{name: "no more matches", query: "本語", start: 4, wantIdx: -1, wantOK: false},
		{name: "no match at all", query: "zzz", start: 0, wantIdx: -1, wantOK: false},
		{name: "empty query matches first", query: "", start: 0, wantIdx: 0, wantEntry: "ls -la", wantOK: true},
		{name: "negative start is out of range", query: "ls", start: -1, wantIdx: -1, wantOK: false},
		{name: "start far beyond history", query: "ls", start: 99, wantIdx: -1, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := NewTerminal(&MockTerminal{}, ">> ")
			term.History = newStubHistory()
			term.search.query = []rune(tt.query)
			term.lock.Lock()
			idx, entry, ok := term.searchBackward(tt.start)
			term.lock.Unlock()
			if idx != tt.wantIdx || ok != tt.wantOK {
				t.Errorf("searchBackward(%d) = (%d,%q,%v), want (%d,%q,%v)",
					tt.start, idx, entry, ok, tt.wantIdx, tt.wantEntry, tt.wantOK)
			}
			if ok && entry != tt.wantEntry {
				t.Errorf("entry = %q, want %q", entry, tt.wantEntry)
			}
		})
	}
}

func TestSearchBackwardEmptyHistory(t *testing.T) {
	term := NewTerminal(&MockTerminal{}, ">> ")
	term.History = &stubHistory{}
	term.search.query = []rune("x")
	term.lock.Lock()
	idx, entry, ok := term.searchBackward(0)
	term.lock.Unlock()
	if ok || idx != -1 || entry != "" {
		t.Errorf("searchBackward on empty history = (%d,%q,%v), want (-1,\"\",false)", idx, entry, ok)
	}
}

func TestSearchPromptRendered(t *testing.T) {
	_, _, received, err := runSearch("\x12", newStubHistory())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if !strings.Contains(received, "(reverse-i-search)'': ") {
		t.Errorf("received %q does not contain the empty search prompt", received)
	}
}

func TestSearchMatchesQuery(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "ascii query", input: "\x12ls", want: "ls -la"},
		{name: "cjk query", input: "\x12本語", want: "日本語のテスト二"},
		{name: "second match via repeated ctrl-r", input: "\x12本語\x12", want: "grep 日本語"},
		{name: "third match via repeated ctrl-r", input: "\x12本語\x12\x12", want: "日本語のテスト"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term, _, received, _ := runSearch(tt.input, newStubHistory())
			if string(term.line) != tt.want {
				t.Errorf("candidate line = %q, want %q", string(term.line), tt.want)
			}
			if !strings.Contains(received, tt.want) {
				t.Errorf("received %q does not contain candidate %q", received, tt.want)
			}
		})
	}
}

func TestSearchFailedKeepsCandidate(t *testing.T) {
	term, _, received, _ := runSearch("\x12本語z", newStubHistory())
	if !term.search.failed {
		t.Errorf("search.failed = false, want true")
	}
	if string(term.line) != "日本語のテスト二" {
		t.Errorf("candidate = %q, want it preserved", string(term.line))
	}
	if !strings.Contains(received, "(failed reverse-i-search)'本語z': ") {
		t.Errorf("received %q does not contain the failed prompt", received)
	}
}

func TestSearchExhaustedByRepeatedCtrlR(t *testing.T) {
	term, _, _, _ := runSearch("\x12本語\x12\x12\x12", newStubHistory())
	if !term.search.failed {
		t.Errorf("search.failed = false after exhausting matches")
	}
	if string(term.line) != "日本語のテスト" {
		t.Errorf("candidate = %q, want the last match preserved", string(term.line))
	}
}

func TestSearchBackspaceShrinksQuery(t *testing.T) {
	// 本語z で一致が消えたあと Backspace で z を消すと、先頭から再検索して復帰する。
	term, _, _, _ := runSearch("\x12本語z\x7f", newStubHistory())
	if string(term.search.query) != "本語" {
		t.Errorf("query = %q, want %q", string(term.search.query), "本語")
	}
	if term.search.failed {
		t.Errorf("search.failed = true, want false after re-search")
	}
	if string(term.line) != "日本語のテスト二" {
		t.Errorf("candidate = %q, want the first match", string(term.line))
	}
}

func TestSearchBackspaceOnEmptyQuery(t *testing.T) {
	term, _, _, _ := runSearch("\x12\x7f", newStubHistory())
	if len(term.search.query) != 0 {
		t.Errorf("query = %q, want empty", string(term.search.query))
	}
	if !term.search.active {
		t.Errorf("search.active = false, want the search to stay open")
	}
}

func TestSearchAbortRestoresLine(t *testing.T) {
	// abc を入力し左矢印でカーソルを 2 へ移してから検索、Ctrl-G で中止する。
	term, _, received, _ := runSearch("abc\x1b[D\x12本語\x07", newStubHistory())
	if string(term.line) != "abc" {
		t.Errorf("line = %q, want %q", string(term.line), "abc")
	}
	if term.pos != 2 {
		t.Errorf("pos = %d, want 2", term.pos)
	}
	if term.search.active {
		t.Errorf("search.active = true after abort")
	}
	if strings.Count(received, ">> ") < 2 {
		t.Errorf("received %q does not show a repaint with the normal prompt", received)
	}
}

func TestSearchAbortedByUnknownKey(t *testing.T) {
	// 単独 ESC は次のバイトが届いた時点で keyUnknown として中止される。
	term, _, _, _ := runSearch("abc\x12本語\x1bZ", newStubHistory())
	if term.search.active {
		t.Errorf("search.active = true, want abort on keyUnknown")
	}
	if string(term.line) != "abc" {
		t.Errorf("line = %q, want the pre-search line restored", string(term.line))
	}
}

func TestSearchEnterCommitsCandidate(t *testing.T) {
	hist := newStubHistory()
	_, line, _, err := runSearch("\x12本語\r", hist)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if line != "日本語のテスト二" {
		t.Errorf("line = %q, want the candidate", line)
	}
	if len(hist.added) != 1 || hist.added[0] != "日本語のテスト二" {
		t.Errorf("History.Add calls = %v, want the candidate added once", hist.added)
	}
}

func TestSearchDelegatesOtherKey(t *testing.T) {
	// 左矢印で検索を終了し、候補行に対する通常編集としてそのキーが処理される。
	_, line, _, err := runSearch("\x12本語\x1b[Dx\r", newStubHistory())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if line != "日本語のテストx二" {
		t.Errorf("line = %q, want the candidate edited before the last rune", line)
	}
}

// 3.4.5 の手順 2。検索終了時に通常プロンプト基準で再描画するため、直後の
// カーソル位置は通常プロンプトを起点にした cellPos と一致する。
func TestSearchEndRepaintsWithNormalPrompt(t *testing.T) {
	term, _, _, _ := runSearch("\x12本語\x1b[D", newStubHistory())
	if term.search.active {
		t.Fatalf("search.active = true, want the search ended")
	}
	wantX, wantY := cellPos([]rune(">> "), term.line[:term.pos], term.termWidth)
	if term.cursorX != wantX || term.cursorY != wantY {
		t.Errorf("cursor = (%d,%d), want (%d,%d) based on the normal prompt",
			term.cursorX, term.cursorY, wantX, wantY)
	}
}

func TestSearchEndResetsHistoryIndex(t *testing.T) {
	// 検索を中止したあとの上矢印は最新履歴から再開する。
	_, line, _, err := runSearch("\x12本語\x12\x07\x1b[A\r", newStubHistory())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if line != "ls -la" {
		t.Errorf("line = %q, want the most recent history entry", line)
	}
}

func TestSearchEndsOnPasteStart(t *testing.T) {
	term, line, _, err := runSearch("\x12本語\x1b[200~ab\x1b[201~\r", newStubHistory())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if term.search.active {
		t.Errorf("search.active = true, want the search ended by paste")
	}
	if line != "日本語のテスト二ab" {
		t.Errorf("line = %q, want the pasted text appended to the candidate", line)
	}
}

func TestSearchStartsWithEmptyCandidate(t *testing.T) {
	term, _, _, _ := runSearch("abc\x12", newStubHistory())
	if len(term.line) != 0 {
		t.Errorf("line = %q, want empty candidate on an empty query", string(term.line))
	}
	if string(term.search.line) != "abc" {
		t.Errorf("saved line = %q, want %q", string(term.search.line), "abc")
	}
	if term.search.index != -1 || term.search.failed {
		t.Errorf("search state = (index %d, failed %v), want (-1, false)", term.search.index, term.search.failed)
	}
}

func TestReverseSearchKeyIsNotPrintable(t *testing.T) {
	for _, key := range []rune{keyReverseSearch, keyAbortSearch} {
		if isPrintable(key) {
			t.Errorf("key %U must not be printable", key)
		}
	}
	if keyReverseSearch == keyAbortSearch {
		t.Errorf("search keys must be distinct")
	}
}

// 検索キーの値そのものを固定する。iota 起点や採番間隔が変わると
// terminal.go 側の surrogate キー (0xd807 以降) と衝突しうるため、
// 「surrogate 領域である」ではなく実値で押さえる。
func TestSearchKeyConstantValues(t *testing.T) {
	tests := []struct {
		name string
		got  rune
		want rune
	}{
		{name: "keyReverseSearch", got: keyReverseSearch, want: 0xd830},
		{name: "keyAbortSearch", got: keyAbortSearch, want: 0xd831},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %#x, want %#x", tt.name, tt.got, tt.want)
			}
		})
	}
	if keyAbortSearch-keyReverseSearch != 1 {
		t.Errorf("採番間隔 = %d, want 1", keyAbortSearch-keyReverseSearch)
	}
	for _, key := range []rune{keyUnknown, keyPasteEnd} {
		if key == keyReverseSearch || key == keyAbortSearch {
			t.Errorf("terminal.go のキー %#x と衝突している", key)
		}
	}
}

// paste 中の 0x12 / 0x07 は制御キーではなく本文として取り込まれる。
func TestSearchKeysAreLiteralDuringPaste(t *testing.T) {
	_, line, _, err := runSearch("\x1b[200~a\x12b\x07c\x1b[201~\r", newStubHistory())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if line != "a\x12b\x07c" {
		t.Errorf("line = %q, want the control bytes kept verbatim", line)
	}
}

// runSearchAt は端末幅を指定して入力を 1 回の ReadLine へ流す。
func runSearchAt(input string, width int, hist History) (*Terminal, string, string, error) {
	mock := &MockTerminal{toSend: []byte(input), bytesPerRead: 1}
	term := NewTerminal(mock, ">> ")
	term.History = hist
	if err := term.SetSize(width, 24); err != nil {
		panic(err)
	}
	line, err := term.ReadLine()
	return term, line, string(mock.received), err
}

// 検索プロンプトは 25 セル以上あり、狭い端末では折返す。再描画経路が
// writeLineCells と同じ折返し規則を通らないと、端末へ送るバイト列に
// パディングが入らず、内部桁位置と実画面が食い違う。
func TestRepaintPaintsPromptWithSharedLayout(t *testing.T) {
	newSearching := func(width int) *Terminal {
		term := NewTerminal(&MockTerminal{}, ">> ")
		if err := term.SetSize(width, 24); err != nil {
			t.Fatalf("SetSize: %v", err)
		}
		term.search.active = true
		term.search.query = []rune("本語")
		return term
	}
	for width := 8; width <= 40; width++ {
		ref := newSearching(width)
		ref.writeLineCells(ref.displayPrompt())
		want := string(ref.outBuf)
		wantX, wantY := cellPos(ref.displayPrompt(), nil, width)
		if ref.cursorX != wantX || ref.cursorY != wantY {
			t.Fatalf("width=%d: writeLineCells cursor = (%d,%d), cellPos = (%d,%d)",
				width, ref.cursorX, ref.cursorY, wantX, wantY)
		}

		term := newSearching(width)
		term.clearAndRepaintLinePlusNPrevious(0)
		if !strings.Contains(string(term.outBuf), want) {
			t.Errorf("width=%d: repaint %q does not paint the prompt as %q",
				width, string(term.outBuf), want)
		}
		if term.cursorX != wantX || term.cursorY != wantY {
			t.Errorf("width=%d: repaint cursor = (%d,%d), want (%d,%d)",
				width, term.cursorX, term.cursorY, wantX, wantY)
		}
	}
}

// 検索中の狭い端末でも、再描画後の桁位置は cellPos と一致する。
func TestSearchRepaintMatchesCellPosOnNarrowTerminal(t *testing.T) {
	for width := 8; width <= 40; width++ {
		term, _, _, _ := runSearchAt("\x12本語", width, newStubHistory())
		wantX, wantY := cellPos(term.displayPrompt(), term.line[:term.pos], width)
		if term.cursorX != wantX || term.cursorY != wantY {
			t.Errorf("width=%d: cursor = (%d,%d), want (%d,%d)",
				width, term.cursorX, term.cursorY, wantX, wantY)
		}
	}
}

// Ctrl-L の再描画も同じ規則を共有する。
func TestClearScreenRepaintMatchesCellPosOnNarrowTerminal(t *testing.T) {
	for width := 8; width <= 40; width++ {
		term, _, _, _ := runSearchAt("\x12本語\x0c", width, newStubHistory())
		wantX, wantY := cellPos(term.displayPrompt(), term.line[:term.pos], width)
		if term.cursorX != wantX || term.cursorY != wantY {
			t.Errorf("width=%d: cursor = (%d,%d), want (%d,%d)",
				width, term.cursorX, term.cursorY, wantX, wantY)
		}
	}
}

func TestCurrentLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		active    bool
		savedLine string
		want      string
	}{
		{name: "not searching", line: "abc", want: "abc"},
		{name: "not searching empty", line: "", want: ""},
		{name: "searching returns saved line", line: "候補", active: true, savedLine: "abc", want: "abc"},
		{name: "searching with empty saved line", line: "候補", active: true, savedLine: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term := NewTerminal(&MockTerminal{}, ">> ")
			term.line = []rune(tt.line)
			term.search.active = tt.active
			term.search.line = []rune(tt.savedLine)
			if got := string(term.currentLine()); got != tt.want {
				t.Errorf("currentLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExitSearchState(t *testing.T) {
	term := NewTerminal(&MockTerminal{}, ">> ")
	term.search = searchState{active: true, query: []rune("x"), index: 2, failed: true, line: []rune("abc"), pos: 3}
	term.exitSearchState()
	if term.search.active || term.search.query != nil || term.search.index != 0 ||
		term.search.failed || term.search.line != nil || term.search.pos != 0 {
		t.Errorf("search = %+v, want the zero value", term.search)
	}
}

// 検索中の Ctrl-D は候補ではなく退避した入力行の空判定で終了を決める。
func TestCtrlDDuringSearchUsesSavedLine(t *testing.T) {
	_, line, _, err := runSearch("abc\x12\x04\r", newStubHistory())
	if err != nil {
		t.Fatalf("err = %v, want nil (saved line is not empty)", err)
	}
	if line != "" {
		t.Errorf("line = %q, want the empty candidate committed", line)
	}
}

func TestCtrlDDuringSearchOnEmptyLineEndsSession(t *testing.T) {
	term, _, _, err := runSearch("\x12\x04", newStubHistory())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if term.search.active {
		t.Errorf("search.active = true, want the state cleared on exit")
	}
}

func TestCtrlCDuringSearchClearsState(t *testing.T) {
	term, _, _, err := runSearch("\x12本語\x03", newStubHistory())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if term.search.active || term.search.query != nil {
		t.Errorf("search = %+v, want the zero value", term.search)
	}
}
