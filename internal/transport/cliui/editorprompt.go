package cliui

import (
	"context"
	"errors"
	"io"
	"strings"

	"golang.org/x/term"
)

// readEditorPrompt は term.Terminal から 1 プロンプト分の入力を読む。
// bracketed paste で貼り付けられた行は ErrPasteIndicator 付きで届くため、
// 通常の Enter で確定した行が届くまで改行で結合し続ける。これにより
// 改行込みの長文ペーストがタイミングに依存せず 1 プロンプトへまとまる
// (貼り付け後、Enter で送信する操作感になる)。
// Ctrl-C / Ctrl-D は term.Terminal が io.EOF として返す。
func readEditorPrompt(t *term.Terminal) (string, error) {
	var parts []string
	for {
		line, err := t.ReadLine()
		switch {
		case err == nil:
			parts = append(parts, line)
			return strings.Join(parts, "\n"), nil
		case errors.Is(err, term.ErrPasteIndicator):
			parts = append(parts, line)
		default:
			return "", err
		}
	}
}

// pumpReader は bytePump を io.Reader として term.Terminal へ渡すアダプタ。
// 生成中に pushback されたバイトを優先して返し、ctx キャンセル (SIGINT) で
// ブロック中の Read を即座にエラー復帰させる。
type pumpReader struct {
	p   *bytePump
	ctx context.Context
}

func (r *pumpReader) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	if len(r.p.pending) > 0 {
		n := copy(buf, r.p.pending)
		r.p.pending = append([]byte{}, r.p.pending[n:]...)
		return n, nil
	}
	select {
	case b, ok := <-r.p.ch:
		if !ok {
			if r.p.err != nil {
				return 0, r.p.err
			}
			return 0, io.EOF
		}
		buf[0] = b
		n := 1
		// 溜まっている分は非ブロッキングでまとめて返し、エスケープ列の分割読みを減らす
		for n < len(buf) {
			select {
			case b2, ok2 := <-r.p.ch:
				if !ok2 {
					return n, nil
				}
				buf[n] = b2
				n++
			default:
				return n, nil
			}
		}
		return n, nil
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
}
