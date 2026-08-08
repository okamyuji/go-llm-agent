package cliui

import (
	"bufio"
	"io"
)

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

// keySource はキーイベントの供給元。next は次のイベントと、継続可否 (ok=false で終端) を返す。
// 同期読み取りの keyReader と、goroutine 経由の chanKeySource の両方がこれを満たす。
type keySource interface {
	next() (keyEvent, bool)
}

// keyReader はバイトストリームをキーイベントへデコードする。
// raw モード端末でも、テストのバイト列でも同じロジックで動く。
type keyReader struct {
	r *bufio.Reader
}

// next は keySource を満たす。ストリーム終端 (読み取りエラー) で ok=false。
// Ctrl-D (0x04) はエラーを伴わない keyEOF なので ok=true のまま返す。
func (k *keyReader) next() (keyEvent, bool) {
	ev, err := k.readKey()
	if err != nil {
		return ev, false
	}
	return ev, true
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

// pushback は受信済みキーを先頭側の待ち行列へ戻す (FIFO を保つ)。
func (b *bufKeySource) pushback(ev keyEvent) {
	b.pushed = append(b.pushed, ev)
}

func newKeyReader(rd io.Reader) *keyReader {
	return &keyReader{r: bufio.NewReader(rd)}
}

// readKey は次のキーイベントを 1 つ返す。ストリーム終端では keyEOF と io.EOF を返す。
func (k *keyReader) readKey() (keyEvent, error) {
	b, err := k.r.ReadByte()
	if err != nil {
		return keyEvent{kind: keyEOF}, err
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
		// その他の制御文字は無視 (未対応キー)
		return keyEvent{kind: keyUnknown}, nil
	case b < 0x80:
		return keyEvent{kind: keyRune, r: rune(b)}, nil
	default:
		// UTF-8 マルチバイト。1 バイト戻して rune として読み直す
		_ = k.r.UnreadByte()
		r, _, rerr := k.r.ReadRune()
		if rerr != nil {
			return keyEvent{kind: keyEOF}, rerr
		}
		return keyEvent{kind: keyRune, r: r}, nil
	}
}

// readEscape は ESC (0x1b) を読んだ後の分岐。矢印など CSI シーケンスを解釈する。
// 続きが CSI でなければ bare ESC 扱いし、読み過ぎた 1 バイトは押し戻す。
func (k *keyReader) readEscape() (keyEvent, error) {
	b2, err := k.r.ReadByte()
	if err != nil {
		// 続きが無い = bare ESC
		return keyEvent{kind: keyEsc}, nil
	}
	if b2 != '[' && b2 != 'O' {
		// ESC + 非シーケンス。bare ESC とし、b2 は次回に回す
		_ = k.r.UnreadByte()
		return keyEvent{kind: keyEsc}, nil
	}
	b3, err := k.r.ReadByte()
	if err != nil {
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
