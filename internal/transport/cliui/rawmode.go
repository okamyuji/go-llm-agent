package cliui

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/term"
)

// errCtrlC は行読み中に Ctrl-C (0x03) を検出したことを示す。
// ターン終了と入力バイト到着の競合で Ctrl-C が生成中の監視をすり抜けても、
// 次の readLine がこのエラーを返すため終了要求は失われない。
var errCtrlC = errors.New("ctrl-c received")

// bytePump は入力を 1 本の goroutine で読み続け、バイト列をチャネルへ流す。
// 行読み (cooked) と生成中の ESC 監視が同じチャネルを時分割で消費するため、
// ブロック中の Read を途中で解除する必要がない。File.Fd() を取得すると
// SetReadDeadline が無効になる (Go の poller から外れる) ので、deadline に
// 頼る停止方式はここでは使えない。
type bytePump struct {
	ch      chan byte
	pending []byte // 生成中に届いた非制御バイトを次の行読みへ引き継ぐ
	err     error  // ch close 後にのみ読む (close が happens-before を与える)
}

// newBytePump は r を読み続ける goroutine を開始する。r が尽きたら ch を閉じる。
func newBytePump(r io.Reader) *bytePump {
	p := &bytePump{ch: make(chan byte, 256)}
	go func() {
		defer close(p.ch)
		buf := make([]byte, 1)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				p.ch <- buf[0]
			}
			if err != nil {
				p.err = err
				return
			}
		}
	}()
	return p
}

// pushback は生成中に消費したバイトを次の readLine の先頭へ戻す。
// runTurn と readLine は同一 goroutine で逐次実行されるため排他は不要。
func (p *bytePump) pushback(b byte) {
	p.pending = append(p.pending, b)
}

// readLine は改行までを 1 行として返す。pushback されたバイトを先に消費する。
// 入力が尽きたら残りを行として返し、空なら読み取りエラー (通常 io.EOF) を返す。
func (p *bytePump) readLine() (string, error) {
	var sb []byte
	for _, b := range p.pending {
		switch b {
		case '\n':
			rest := p.pending[len(sb)+1:]
			line := trimTrailingCR(sb)
			p.pending = append([]byte{}, rest...)
			return line, nil
		case 0x03:
			p.pending = nil
			return "", errCtrlC
		}
		sb = append(sb, b)
	}
	p.pending = nil
	for b := range p.ch {
		switch b {
		case '\n':
			return trimTrailingCR(sb), nil
		case 0x03:
			return "", errCtrlC
		}
		sb = append(sb, b)
	}
	if len(sb) > 0 {
		return trimTrailingCR(sb), nil
	}
	if p.err != nil {
		return "", p.err
	}
	return "", io.EOF
}

// readPrompt はパイプ入力向けに 1 行 = 1 プロンプトで読む (端末入力は term.Terminal が担当)。
// readLine との違いは ctx キャンセル (SIGINT) でブロック中でも即座に返ること。
// pushback されたバイトを先に消費する。
func (p *bytePump) readPrompt(ctx context.Context) (string, error) {
	var line []byte
	for {
		var b byte
		if len(p.pending) > 0 {
			b = p.pending[0]
			p.pending = append([]byte{}, p.pending[1:]...)
		} else {
			select {
			case nb, ok := <-p.ch:
				if !ok {
					if len(line) > 0 {
						return trimTrailingCR(line), nil
					}
					if p.err != nil {
						return "", p.err
					}
					return "", io.EOF
				}
				b = nb
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		switch b {
		case '\n':
			return trimTrailingCR(line), nil
		case 0x03:
			return "", errCtrlC
		default:
			line = append(line, b)
		}
	}
}

// errESC は承認プロンプト中に ESC (0x1b) を受け取ったことを表す。
// 生成中の ESC と同じくターン中断を意味する
var errESC = errors.New("cliui: ESC pressed")

// readAnswerLine は承認プロンプトの y/n 応答を 1 行読む。pushback 済みバイトを先に消費し、
// ctx が Done になった場合は即座に ctx.Err() を返す。
// 0x03 (Ctrl-C) は errCtrlC、0x1b (ESC) は errESC を返す。呼び出し元がこの 2 つを
// 終了・中断へ振り分ける。ESC を通常バイトとして積むと、承認待ちの間だけ ESC 中断が
// 効かなくなるため専用の分岐を持つ
func (p *bytePump) readAnswerLine(ctx context.Context) (string, error) {
	var sb []byte
	for i, b := range p.pending {
		switch b {
		case '\n':
			rest := p.pending[i+1:]
			p.pending = append([]byte{}, rest...)
			return trimTrailingCR(sb), nil
		case 0x03:
			p.pending = nil
			return "", errCtrlC
		case 0x1b:
			p.pending = nil
			return "", errESC
		default:
			sb = append(sb, b)
		}
	}
	p.pending = nil
	for {
		select {
		case b, ok := <-p.ch:
			if !ok {
				if len(sb) > 0 {
					return trimTrailingCR(sb), nil
				}
				if p.err != nil {
					return "", p.err
				}
				return "", io.EOF
			}
			switch b {
			case '\n':
				return trimTrailingCR(sb), nil
			case 0x03:
				return "", errCtrlC
			case 0x1b:
				return "", errESC
			default:
				sb = append(sb, b)
			}
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func trimTrailingCR(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == '\r' {
		return string(b[:n-1])
	}
	return string(b)
}

// beginRaw は端末を raw にし、cooked へ戻す関数を返す。端末でなければ no-op。
// バイトの読み取り自体は bytePump が担うため、ここではモード切替だけを行う。
func beginRaw(f *os.File) (restore func(), ok bool) {
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return func() {}, false
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}, false
	}
	return func() { _ = term.Restore(fd, st) }, true
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
