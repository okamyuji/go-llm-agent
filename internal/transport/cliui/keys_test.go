package cliui

import (
	"strings"
	"testing"
)

func TestKeyReaderDecodesSequences(t *testing.T) {
	cases := []struct {
		name  string
		input string
		kind  keyKind
		r     rune
	}{
		{"ascii rune", "a", keyRune, 'a'},
		{"enter CR", "\r", keyEnter, 0},
		{"enter LF", "\n", keyEnter, 0},
		{"backspace DEL", "\x7f", keyBackspace, 0},
		{"backspace BS", "\x08", keyBackspace, 0},
		{"ctrl-c", "\x03", keyCtrlC, 0},
		{"ctrl-d", "\x04", keyEOF, 0},
		{"up", "\x1b[A", keyUp, 0},
		{"down", "\x1b[B", keyDown, 0},
		{"right", "\x1b[C", keyRight, 0},
		{"left", "\x1b[D", keyLeft, 0},
		{"bare esc", "\x1b", keyEsc, 0},
		{"japanese rune", "あ", keyRune, 'あ'},
		{"fullwidth rune", "Ａ", keyRune, 'Ａ'},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kr := newKeyReader(strings.NewReader(c.input))
			ev, err := kr.readKey()
			if err != nil && c.kind != keyEOF {
				t.Fatalf("readKey err=%v", err)
			}
			if ev.kind != c.kind {
				t.Errorf("kind=%d, want %d", ev.kind, c.kind)
			}
			if c.r != 0 && ev.r != c.r {
				t.Errorf("rune=%q, want %q", ev.r, c.r)
			}
		})
	}
}

func TestKeyReaderStreamOfKeys(t *testing.T) {
	// "あ" + Left + Backspace + Enter を順に読む
	kr := newKeyReader(strings.NewReader("あ\x1b[D\x7f\r"))
	want := []keyKind{keyRune, keyLeft, keyBackspace, keyEnter}
	for i, wk := range want {
		ev, err := kr.readKey()
		if err != nil {
			t.Fatalf("readKey[%d] err=%v", i, err)
		}
		if ev.kind != wk {
			t.Errorf("key[%d] kind=%d, want %d", i, ev.kind, wk)
		}
	}
}

func TestKeyReaderEscThenRune(t *testing.T) {
	// ESC の直後が [ でない場合は bare ESC 扱いし、次のバイトは失わない
	kr := newKeyReader(strings.NewReader("\x1bx"))
	ev1, _ := kr.readKey()
	if ev1.kind != keyEsc {
		t.Fatalf("first kind=%d, want keyEsc", ev1.kind)
	}
	ev2, err := kr.readKey()
	if err != nil {
		t.Fatalf("second readKey err=%v", err)
	}
	if ev2.kind != keyRune || ev2.r != 'x' {
		t.Errorf("second=%v/%q, want keyRune/x", ev2.kind, ev2.r)
	}
}

func TestKeyReaderBareEscViaTimeout(t *testing.T) {
	// raw 端末経路 (バイトチャネル) で、ESC の後に続きが来なければ escTimeout で keyEsc を返す。
	// io.Reader 経路の EOF 依存ではなく、実端末で単独 ESC が届くことを担保する。
	ch := make(chan byte, 4)
	ch <- 0x1b // ESC のみ。以降のバイトは送らない
	kr := newKeyReaderFromBytes(ch)
	ev, err := kr.readKey()
	if err != nil {
		t.Fatalf("readKey err=%v", err)
	}
	if ev.kind != keyEsc {
		t.Errorf("kind=%d, want keyEsc (timeout path)", ev.kind)
	}
}

func TestKeyReaderArrowViaChan(t *testing.T) {
	// バイトチャネル経路でも矢印 (連続到着) を正しく解釈する。
	ch := make(chan byte, 8)
	for _, b := range []byte{0x1b, '[', 'A'} {
		ch <- b
	}
	kr := newKeyReaderFromBytes(ch)
	ev, _ := kr.readKey()
	if ev.kind != keyUp {
		t.Errorf("kind=%d, want keyUp", ev.kind)
	}
}
