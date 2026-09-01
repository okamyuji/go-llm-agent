package cliui

import (
	"context"
	"io"

	"github.com/okamyuji/go-llm-agent/internal/transport/cliui/lineedit"
)

// readEditorPrompt は lineedit.Terminal から 1 プロンプト分の入力を読む。
// bracketed paste は lineedit.Terminal 側で 1 プロンプトへまとまる
// (複数行ペーストは短縮表示になり、Enter で原文へ展開されて届く)。
// Ctrl-C / Ctrl-D は lineedit.Terminal が io.EOF として返す。
func readEditorPrompt(t *lineedit.Terminal) (string, error) {
	return t.ReadLine()
}

// pumpReader は bytePump を io.Reader として lineedit.Terminal へ渡すアダプタ。
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
