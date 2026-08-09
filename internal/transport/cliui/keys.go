package cliui

import (
	"bufio"
	"io"
	"time"
	"unicode/utf8"
)

// escTimeout は ESC 単独押下と矢印等の CSI シーケンスを区別する待ち時間。
// raw 端末では矢印キーは複数バイトが一括で届くため、この時間内に続きが来なければ bare ESC とみなす。
const escTimeout = 40 * time.Millisecond

// keyKind はデコードされたキー種別。
type keyKind int

const (
	keyRune keyKind = iota // 表示可能な 1 文字 (r に格納)
	keyEnter
	keyBackspace
	keyLeft
	keyRight
	keyUp
	keyDown
	keyEsc
	keyCtrlC
	keyEOF // Ctrl-D または入力ストリーム終端
	keyUnknown
)

// keyEvent は 1 回のキー入力。
type keyEvent struct {
	kind keyKind
	r    rune
}

// keySource はキーイベントの供給元。next は次のイベントと継続可否 (ok=false で終端) を返す。
type keySource interface {
	next() (keyEvent, bool)
}

// byteReader はキー解読の下位バイト供給。peekByte はタイムアウト付きで、
// ESC 単独と CSI シーケンスの区別に使う。
type byteReader interface {
	readByte() (byte, bool)
	peekByte(d time.Duration) (byte, bool)
}

// ioByteReader は io.Reader を包む byteReader。テスト用。peekByte はタイムアウトを持たず、
// ストリーム終端 (EOF) を「続きなし」とみなすので bare ESC を決定的に扱える。
type ioByteReader struct {
	r *bufio.Reader
}

func (b *ioByteReader) readByte() (byte, bool) {
	c, err := b.r.ReadByte()
	if err != nil {
		return 0, false
	}
	return c, true
}

func (b *ioByteReader) peekByte(time.Duration) (byte, bool) { return b.readByte() }

// chanByteReader はバイトチャネルを包む byteReader。raw 端末用。
// peekByte は select でタイムアウトを実現し、ESC 単独をブロックせず検出する。
type chanByteReader struct {
	ch <-chan byte
}

func (b *chanByteReader) readByte() (byte, bool) {
	c, ok := <-b.ch
	return c, ok
}

func (b *chanByteReader) peekByte(d time.Duration) (byte, bool) {
	select {
	case c, ok := <-b.ch:
		return c, ok
	case <-time.After(d):
		return 0, false
	}
}

// keyReader はバイト供給をキーイベントへデコードする。
// pending は ESC 判定で読み過ぎた 1 バイトの押し戻し先 (hasPending で有無を表す)。
type keyReader struct {
	br         byteReader
	pending    byte
	hasPending bool
}

func newKeyReader(rd io.Reader) *keyReader {
	return &keyReader{br: &ioByteReader{r: bufio.NewReader(rd)}}
}

// newKeyReaderFromBytes は raw 端末のバイトチャネルからキーを解読する。
func newKeyReaderFromBytes(ch <-chan byte) *keyReader {
	return &keyReader{br: &chanByteReader{ch: ch}}
}

func (k *keyReader) rb() (byte, bool) {
	if k.hasPending {
		b := k.pending
		k.hasPending = false
		return b, true
	}
	return k.br.readByte()
}

func (k *keyReader) pb(d time.Duration) (byte, bool) {
	if k.hasPending {
		b := k.pending
		k.hasPending = false
		return b, true
	}
	return k.br.peekByte(d)
}

// next は keySource を満たす。終端では keyEOF と ok=false。
func (k *keyReader) next() (keyEvent, bool) {
	ev, err := k.readKey()
	if err != nil {
		return ev, false
	}
	return ev, true
}

// readKey は次のキーイベントを 1 つ返す。ストリーム終端では keyEOF と io.EOF を返す。
func (k *keyReader) readKey() (keyEvent, error) {
	b, ok := k.rb()
	if !ok {
		return keyEvent{kind: keyEOF}, io.EOF
	}
	switch {
	case b == '\r' || b == '\n':
		return keyEvent{kind: keyEnter}, nil
	case b == 0x7f || b == 0x08:
		return keyEvent{kind: keyBackspace}, nil
	case b == 0x03:
		return keyEvent{kind: keyCtrlC}, nil
	case b == 0x04:
		return keyEvent{kind: keyEOF}, nil
	case b == 0x1b:
		return k.readEscape()
	case b < 0x20:
		return keyEvent{kind: keyUnknown}, nil
	case b < 0x80:
		return keyEvent{kind: keyRune, r: rune(b)}, nil
	default:
		return k.readUTF8(b)
	}
}

// readEscape は ESC (0x1b) の後を解釈する。escTimeout 内に続きが来なければ bare ESC。
// CSI ('[' / 'O') なら矢印等を解釈し、それ以外の 1 バイトは押し戻して bare ESC とする。
func (k *keyReader) readEscape() (keyEvent, error) {
	b2, ok := k.pb(escTimeout)
	if !ok {
		return keyEvent{kind: keyEsc}, nil
	}
	if b2 != '[' && b2 != 'O' {
		k.pending = b2
		k.hasPending = true
		return keyEvent{kind: keyEsc}, nil
	}
	b3, ok := k.pb(escTimeout)
	if !ok {
		return keyEvent{kind: keyEsc}, nil
	}
	switch b3 {
	case 'A':
		return keyEvent{kind: keyUp}, nil
	case 'B':
		return keyEvent{kind: keyDown}, nil
	case 'C':
		return keyEvent{kind: keyRight}, nil
	case 'D':
		return keyEvent{kind: keyLeft}, nil
	default:
		return keyEvent{kind: keyUnknown}, nil
	}
}

// readUTF8 はマルチバイト文字の先頭バイト b から残りを読み rune を返す。
func (k *keyReader) readUTF8(b byte) (keyEvent, error) {
	n := utf8ByteLen(b)
	buf := make([]byte, 1, 4)
	buf[0] = b
	for i := 1; i < n; i++ {
		c, ok := k.rb()
		if !ok {
			break
		}
		buf = append(buf, c)
	}
	r, _ := utf8.DecodeRune(buf)
	return keyEvent{kind: keyRune, r: r}, nil
}

// utf8ByteLen は UTF-8 先頭バイトから総バイト数を返す (不正なら 1)。
func utf8ByteLen(b byte) int {
	switch {
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 1
	}
}

// bufKeySource はチャネル供給に押し戻しバッファを備えた keySource。
// 単一の入力 goroutine がキーを流し込み、行編集と生成中 ESC 監視が時分割で消費する。
// 生成中に受け取った非 ESC キーは pushback で退避し、次の行編集へ順序どおり引き継ぐ。
type bufKeySource struct {
	ch     <-chan keyEvent
	pushed []keyEvent
}

func (b *bufKeySource) next() (keyEvent, bool) {
	if len(b.pushed) > 0 {
		ev := b.pushed[0]
		b.pushed = b.pushed[1:]
		return ev, true
	}
	ev, ok := <-b.ch
	return ev, ok
}

// pushback は受信済みキーを待ち行列の末尾へ追加する。next は先頭から取り出すので FIFO を保つ。
func (b *bufKeySource) pushback(ev keyEvent) {
	b.pushed = append(b.pushed, ev)
}
