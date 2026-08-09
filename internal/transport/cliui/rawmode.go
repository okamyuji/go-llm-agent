package cliui

import (
	"io"
	"os"

	"golang.org/x/term"
)

// enableRawMode は in が端末なら raw モードにし、復元関数と有効フラグを返す。
// in が *os.File でない (テストの strings.Reader 等) 場合は何もしない。
func enableRawMode(in io.Reader) (restore func(), enabled bool) {
	f, ok := in.(*os.File)
	if !ok {
		return func() {}, false
	}
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

// crlfWriter は raw モードで出力する際、単独の \n を \r\n へ変換する。
// term.MakeRaw は出力後処理 (ONLCR) を無効化するため、モデル出力等に含まれる \n が
// 桁を戻さずに段だけ下がるのを防ぐ。直前が \r の場合は二重にしない。
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
