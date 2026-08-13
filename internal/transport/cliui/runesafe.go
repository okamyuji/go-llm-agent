package cliui

import "io"

// runeSafeWriter は書き込まれたバイト列の末尾が UTF-8 マルチバイト文字の
// 途中で切れている場合、その末尾バイト列を次の Write まで保持し、完結した
// バイト列のみを下流へ書く。ストリーミング応答のチャンクが文字の途中で
// 分割されても、端末へ不完全な UTF-8 を書かないようにするために使う。
type runeSafeWriter struct {
	w   io.Writer
	buf []byte // 前回までに保持している不完全な末尾バイト列（0〜3 バイト）
}

// newRuneSafeWriter w への書込みを rune 境界で保護する writer を作る
func newRuneSafeWriter(w io.Writer) *runeSafeWriter {
	return &runeSafeWriter{w: w}
}

// Write p を末尾の保持バイトと結合し、完結した部分だけを w へ書く。
// 戻り値の n は、成功時は len(p)（呼び出し元は p 全体を消費済みとして扱ってよい）、
// 下流の w.Write が失敗した場合は 0 とし、そのエラーをそのまま返す。
// 失敗時は保持バイトを空にする。このとき、前回の Write から持ち越していた
// 不完全な末尾バイト列は complete 側に含まれたまま失われる。呼び出し元
// (runTurn) は失敗した delta を再送しないため、既存の
// io.WriteString(out, ev.Delta) と同じく失敗分は失われる契約であり、
// 持ち越し分についても同じ扱いとする。次回の Write が壊れたバイト列を
// 再送しないことを優先する。
func (s *runeSafeWriter) Write(p []byte) (n int, err error) {
	data := append(s.buf, p...) //nolint:gocritic // s.buf を nil 化する前に別変数 data へ結合する意図的な設計
	s.buf = nil
	complete, pending := splitIncompleteTail(data)
	if len(complete) > 0 {
		if _, werr := s.w.Write(complete); werr != nil {
			return 0, werr
		}
	}
	s.buf = pending
	return len(p), nil
}

// Flush 保持中のバイト列があれば、完結しているかに関わらずそのまま w へ書く。
// ターン終了・エラー・中断・他メッセージの書込み直前など、出力順序を守る
// 必要があるすべての箇所で呼ぶ。壊れた UTF-8 を永久に保持し続けて出力が
// 失われることを避けるため、不完全なままでも最終的には吐き出す。
func (s *runeSafeWriter) Flush() error {
	if len(s.buf) == 0 {
		return nil
	}
	b := s.buf
	s.buf = nil
	_, err := s.w.Write(b)
	return err
}

// leadByteLen c が UTF-8 の先頭バイトである場合、そのシーケンス全体の
// バイト長（1〜4）を返す。継続バイト（0x80-0xBF）や不正な先頭パターンは 0 を返す。
func leadByteLen(c byte) int {
	switch {
	case c&0x80 == 0x00:
		return 1
	case c&0xE0 == 0xC0:
		return 2
	case c&0xF0 == 0xE0:
		return 3
	case c&0xF8 == 0xF0:
		return 4
	default:
		return 0
	}
}

// splitIncompleteTail data の末尾が UTF-8 マルチバイト文字の途中で切れて
// いる場合、その未完成な末尾部分を pending として切り出し、残りを complete
// として返す。末尾から最大 3 バイト遡って先頭バイトを探す（4 バイト文字の
// 最大不足は 3 バイトのため）。妥当な先頭バイトが見つからない場合（ASCII
// で終わっている、または不正なバイト列）は data 全体を complete として返す。
func splitIncompleteTail(data []byte) (complete, pending []byte) {
	n := len(data)
	if n == 0 {
		return data, nil
	}
	limit := 3
	if n < limit {
		limit = n
	}
	for i := 1; i <= limit; i++ {
		c := data[n-i]
		need := leadByteLen(c)
		if need == 0 {
			// 継続バイトまたは非 UTF-8 バイト。さらに 1 バイト前を確認する
			continue
		}
		if need > i {
			// 先頭バイトから末尾までの長さがシーケンス長に満たない = 不完全
			return data[:n-i], data[n-i:]
		}
		// シーケンスが末尾バイトの手前で完結している（例: 3 バイト文字が
		// ちょうど収まっている）。これ以上遡る必要はない
		break
	}
	return data, nil
}
