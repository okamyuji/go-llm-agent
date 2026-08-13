package cliui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// readPrompt は 1 プロンプト分の入力を返す。最初の行を読んだ後、coalesce の時間窓内に
// 連続到着した行を改行で結合する。改行込みの長文ペーストは端末 (rlwrap 経由含む) から
// 短時間のバーストで届くため 1 プロンプトにまとまり、手入力の連続質問は窓を超えるので
// 行単位になる。coalesce が 0 以下なら 1 行 = 1 プロンプト (パイプ入力の互換維持)。
// 窓内に改行まで届かなかった末尾バイトは消費せず pending へ戻す。
//
// 端末は beginPromptMode により非カノニカルで読むため (カノニカルの 1024 バイト行制限で
// 長文ペーストが詰まるのを避ける)、行編集も自前で行う: BS/DEL は rune 単位の削除、
// 空入力での Ctrl-D は EOF、\r 単独と \r\n は行末。echo 非 nil なら入力を書き戻す
// (非カノニカルではカーネル echo が無効のため)。ctx キャンセル (SIGINT) で即座に返る。
func (p *bytePump) readPrompt(ctx context.Context, coalesce time.Duration, echo io.Writer) (string, error) {
	echoStr := func(s string) {
		if echo != nil {
			_, _ = io.WriteString(echo, s)
		}
	}
	var prompt []byte // 確定済みの行 (\n 区切りで蓄積)
	var line []byte   // 編集中の行
	firstLineDone := false
	lastWasCR := false
	endLine := func() {
		prompt = append(prompt, trimTrailingCR(line)...)
		prompt = append(prompt, '\n')
		line = line[:0:0]
		firstLineDone = true
		echoStr("\n")
	}
	finish := func() (string, error) {
		return strings.TrimSuffix(string(prompt), "\n"), nil
	}
	for {
		var b byte
		switch {
		case len(p.pending) > 0:
			b = p.pending[0]
			p.pending = append([]byte{}, p.pending[1:]...)
		case !firstLineDone:
			// 最初の行は時間制限なしで待つ
			select {
			case nb, ok := <-p.ch:
				if !ok {
					if len(line) > 0 {
						endLine()
						return finish()
					}
					if len(prompt) > 0 {
						return finish()
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
		default:
			if coalesce <= 0 {
				return finish()
			}
			select {
			case nb, ok := <-p.ch:
				if !ok {
					if len(line) > 0 {
						endLine()
					}
					return finish()
				}
				b = nb
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(coalesce):
				// 未完の行は消費せず次のプロンプトへ持ち越す
				p.pending = append(line, p.pending...)
				return finish()
			}
		}
		wasCR := lastWasCR
		lastWasCR = b == '\r'
		switch b {
		case '\n':
			if wasCR {
				continue // \r で行末処理済み (\r\n)
			}
			endLine()
			if coalesce <= 0 {
				return finish()
			}
		case '\r':
			endLine()
			if coalesce <= 0 {
				return finish()
			}
		case 0x03: // Ctrl-C
			return "", errCtrlC
		case 0x04: // Ctrl-D: 空入力なら EOF、途中なら無視 (cooked の挙動に合わせる)
			if len(prompt) == 0 && len(line) == 0 {
				return "", io.EOF
			}
		case 0x7f, 0x08: // BS/DEL: 編集中の行から最後の rune を消す
			if len(line) == 0 {
				continue
			}
			r, size := utf8.DecodeLastRune(line)
			line = line[:len(line)-size]
			// 消去 echo。マルチバイト rune は全角幅 (2 桁) とみなす近似で十分
			width := 1
			if r > unicode.MaxASCII {
				width = 2
			}
			echoStr(strings.Repeat("\b \b", width))
		default:
			line = append(line, b)
			// string(b) は byte→rune 変換で UTF-8 を壊すため生バイトのまま書く
			if echo != nil {
				_, _ = echo.Write([]byte{b})
			}
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
