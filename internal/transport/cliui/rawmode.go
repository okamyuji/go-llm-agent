package cliui

import (
	"io"
	"os"
	"time"

	"golang.org/x/term"
)

// readInputLine は io.Reader から 1 行 (改行まで) を 1 バイトずつ読む。bufio を使わないので
// 端末が cooked モードのままでも読み過ぎ (type-ahead の取りこぼし) が起きず、生成中の
// 直接読み取りと共存できる。cooked モードでは端末と IME が描画・編集・折り返しを担う。
func readInputLine(r io.Reader) (string, error) {
	var sb []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			c := buf[0]
			if c == '\n' {
				return trimTrailingCR(sb), nil
			}
			sb = append(sb, c)
		}
		if err != nil {
			if len(sb) > 0 {
				return trimTrailingCR(sb), nil
			}
			return "", err
		}
	}
}

func trimTrailingCR(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return string(b[:n-1])
	}
	return string(b)
}

// handleCancelByte は生成中に受け取った 1 バイトを判定する。ESC は中断、Ctrl-C は終了。
func handleCancelByte(b byte, onEsc, onCtrlC func()) {
	switch b {
	case 0x1b:
		onEsc()
	case 0x03:
		onCtrlC()
	}
}

// scanForCancel は r からバイトを読み、ESC / Ctrl-C を検出してコールバックを呼ぶ。
// io.Reader なので pipe でユニットテストできる。r が尽きたら戻る。
func scanForCancel(r io.Reader, onEsc, onCtrlC func()) {
	buf := make([]byte, 64)
	for {
		n, err := r.Read(buf)
		for i := 0; i < n; i++ {
			handleCancelByte(buf[i], onEsc, onCtrlC)
		}
		if err != nil {
			return
		}
	}
}

// rawTurn は 1 ターンのあいだ端末を raw にし、ESC / Ctrl-C を監視する。
// 入力行は cooked のまま (IME を壊さない) で、生成中だけ raw 化するのが狙い。
type rawTurn struct {
	f        *os.File
	oldState *term.State
	stop     chan struct{}
	done     chan struct{}
}

// beginRawTurn は端末を raw にし、ESC / Ctrl-C 監視 goroutine を開始する。
// 端末でなければ (nil,false)。SetReadDeadline で定期的に読みを解除し、end で確実に止める。
func beginRawTurn(f *os.File, onEsc, onCtrlC func()) (*rawTurn, bool) {
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return nil, false
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false
	}
	rt := &rawTurn{f: f, oldState: st, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(rt.done)
		buf := make([]byte, 64)
		for {
			select {
			case <-rt.stop:
				return
			default:
			}
			_ = f.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
			n, err := f.Read(buf)
			for i := 0; i < n; i++ {
				handleCancelByte(buf[i], onEsc, onCtrlC)
			}
			if err != nil && !os.IsTimeout(err) {
				return
			}
		}
	}()
	return rt, true
}

// end は監視を止め、read deadline を解除して端末を cooked に戻す。
func (rt *rawTurn) end() {
	close(rt.stop)
	_ = rt.f.SetReadDeadline(time.Now()) // 保留中の Read を解除
	<-rt.done
	_ = rt.f.SetReadDeadline(time.Time{}) // deadline 解除
	_ = term.Restore(int(rt.f.Fd()), rt.oldState)
}

// crlfWriter は raw 生成中に単独の \n を \r\n へ変換する。cooked 復帰後は使わない。
type crlfWriter struct {
	w    io.Writer
	last byte
}

func newCRLFWriter(w io.Writer) *crlfWriter { return &crlfWriter{w: w} }

func (c *crlfWriter) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p)+8)
	for _, b := range p {
		if b == '\n' && c.last != '\r' {
			out = append(out, '\r')
		}
		out = append(out, b)
		c.last = b
	}
	if _, err := c.w.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}
