package lineedit

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pasteWrap content を bracketed paste マーカーで包んだ入力バイト列を返す
func pasteWrap(content string) string {
	return "\x1b[200~" + content + "\x1b[201~"
}

// TestReadLine_MultilinePasteCollapsesToPlaceholder 複数行ペーストは 1 回の
// ReadLine で全文が返り、画面には短縮表示だけが出る
func TestReadLine_MultilinePasteCollapsesToPlaceholder(t *testing.T) {
	c := &MockTerminal{toSend: []byte(pasteWrap("aaa\rbbb\rccc") + "\r")}
	ss := NewTerminal(c, "> ")
	line, err := ss.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine err=%v", err)
	}
	if line != "aaa\nbbb\nccc" {
		t.Fatalf("line=%q, want %q", line, "aaa\nbbb\nccc")
	}
	echo := string(c.received)
	if !strings.Contains(echo, "[pasted #1") {
		t.Fatalf("短縮表示が無い: %q", echo)
	}
	if strings.Contains(echo, "bbb") {
		t.Fatalf("ペースト本文が echo されている: %q", echo)
	}
}

// TestReadLine_MultilinePasteHistoryKeepsPlaceholder 履歴ファイルは 1 行
// 1 エントリのため、複数行ペーストは短縮表示のまま履歴へ入る
func TestReadLine_MultilinePasteHistoryKeepsPlaceholder(t *testing.T) {
	c := &MockTerminal{toSend: []byte(pasteWrap("aaa\rbbb") + "\r")}
	ss := NewTerminal(c, "> ")
	if _, err := ss.ReadLine(); err != nil {
		t.Fatalf("ReadLine err=%v", err)
	}
	if ss.History.Len() == 0 {
		t.Fatal("履歴が空")
	}
	got := ss.History.At(0)
	if strings.Contains(got, "\n") {
		t.Fatalf("履歴に改行が入っている: %q", got)
	}
	if !strings.Contains(got, "[pasted #1") {
		t.Fatalf("履歴が短縮表示でない: %q", got)
	}
}

// TestReadLine_ShortSingleLinePasteInsertsLiterally 短い 1 行ペーストは
// そのまま行へ挿入され、タイプ入力と混在できる
func TestReadLine_ShortSingleLinePasteInsertsLiterally(t *testing.T) {
	c := &MockTerminal{toSend: []byte(pasteWrap("hello") + " world\r")}
	ss := NewTerminal(c, "> ")
	line, err := ss.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine err=%v", err)
	}
	if line != "hello world" {
		t.Fatalf("line=%q, want %q", line, "hello world")
	}
}

// TestReadLine_PlaceholderMixedWithTypedText ペースト後にタイプした文字は
// 展開後の全文と結合されて返る
func TestReadLine_PlaceholderMixedWithTypedText(t *testing.T) {
	c := &MockTerminal{toSend: []byte("説明: " + pasteWrap("l1\rl2") + " を確認して\r")}
	ss := NewTerminal(c, "> ")
	line, err := ss.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine err=%v", err)
	}
	want := "説明: l1\nl2 を確認して"
	if line != want {
		t.Fatalf("line=%q, want %q", line, want)
	}
}

// TestReadLine_LongSingleLinePasteCollapsesAndPreserved maxLineLength (4096)
// を超える 1 行ペーストも切り捨てずに全文が返る
func TestReadLine_LongSingleLinePasteCollapsesAndPreserved(t *testing.T) {
	content := strings.Repeat("abcdefghij", 520) // 5200 文字
	c := &MockTerminal{toSend: []byte(pasteWrap(content) + "\r")}
	ss := NewTerminal(c, "> ")
	line, err := ss.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine err=%v", err)
	}
	if line != content {
		t.Fatalf("len(line)=%d, want %d (全文保持)", len(line), len(content))
	}
	if !strings.Contains(string(c.received), "[pasted #1") {
		t.Fatalf("長大 1 行ペーストが短縮表示されていない")
	}
}

// TestReadLine_CtrlCDuringPasteAborts ペースト中の Ctrl-C は取り込みを破棄して
// io.EOF を返す (pasteActive が残留してロックアウトする事故の逃げ道)
func TestReadLine_CtrlCDuringPasteAborts(t *testing.T) {
	c := &MockTerminal{toSend: []byte("\x1b[200~abc\x03def\r")}
	ss := NewTerminal(c, "> ")
	line, err := ss.ReadLine()
	if err != io.EOF {
		t.Fatalf("err=%v, want io.EOF", err)
	}
	if line != "" {
		t.Fatalf("line=%q, want empty", line)
	}
}

// TestReadLine_EscBytesInsidePasteDoNotSwallowPasteEnd ペースト本文中の生 ESC
// バイト (ANSI ログのコピー等) が終了マーカーを食い潰さず、内容として保持される
func TestReadLine_EscBytesInsidePasteDoNotSwallowPasteEnd(t *testing.T) {
	content := "a\x1b[31mred\x1b[0m tail \x1b"
	c := &MockTerminal{toSend: []byte(pasteWrap(content) + "\r")}
	ss := NewTerminal(c, "> ")
	line, err := ss.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine err=%v", err)
	}
	if line != content {
		t.Fatalf("line=%q, want %q", line, content)
	}
}

// TestAddPasteRune_CapsAtPasteMaxRunes 取り込み上限に達したら超過分を破棄する
func TestAddPasteRune_CapsAtPasteMaxRunes(t *testing.T) {
	ss := NewTerminal(&MockTerminal{}, "> ")
	ss.pasteBuf = make([]rune, pasteMaxRunes)
	ss.addPasteRune('x')
	if len(ss.pasteBuf) != pasteMaxRunes {
		t.Fatalf("len(pasteBuf)=%d, want %d (上限で打ち切り)", len(ss.pasteBuf), pasteMaxRunes)
	}
}

// TestInsertRunes_StopsAtMaxLineLength 行長上限に達したら挿入を打ち切る
func TestInsertRunes_StopsAtMaxLineLength(t *testing.T) {
	ss := NewTerminal(&MockTerminal{}, "> ")
	ss.echo = false
	ss.line = make([]rune, maxLineLength)
	ss.pos = maxLineLength
	ss.insertRunes([]rune("ab"))
	if len(ss.line) != maxLineLength {
		t.Fatalf("len(line)=%d, want %d (上限で打ち切り)", len(ss.line), maxLineLength)
	}
}

// TestReadLine_PasteFixturesRoundTrip tests/pastedata の全 fixture が
// ペースト経由で内容を失わず 1 プロンプトへまとまる (CRLF は LF へ正規化)
func TestReadLine_PasteFixturesRoundTrip(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "..", "tests", "pastedata")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("fixture dir 読み込み失敗: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("fixture 読み込み失敗: %v", err)
			}
			want := strings.ReplaceAll(string(raw), "\r\n", "\n")
			// 端末は貼り付け時に改行を CR で送ることが多いため、その形へ変換して流す
			sent := strings.ReplaceAll(want, "\n", "\r")
			c := &MockTerminal{toSend: []byte(pasteWrap(sent) + "\r")}
			ss := NewTerminal(c, "> ")
			line, err := ss.ReadLine()
			if err != nil {
				t.Fatalf("ReadLine err=%v", err)
			}
			if line != want {
				t.Fatalf("fixture %s: 内容が変化 (len got=%d want=%d)\ngot=%q", e.Name(), len(line), len(want), line)
			}
		})
	}
}
