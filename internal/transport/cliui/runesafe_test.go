package cliui

import (
	"bytes"
	"errors"
	"testing"
	"testing/quick"
	"unicode/utf8"
)

func TestRuneSafeWriter_ASCII_SingleWrite(t *testing.T) {
	var buf bytes.Buffer
	w := newRuneSafeWriter(&buf)
	n, err := w.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != 5 {
		t.Fatalf("n = %d, want 5", n)
	}
	if buf.String() != "hello" {
		t.Fatalf("buf = %q, want %q", buf.String(), "hello")
	}
	if len(w.buf) != 0 {
		t.Fatalf("w.buf = %v, want empty", w.buf)
	}
}

func TestRuneSafeWriter_ThreeByteChar_SplitAt1And2(t *testing.T) {
	// "あ" = E3 81 82
	full := []byte("あ")
	var buf bytes.Buffer
	w := newRuneSafeWriter(&buf)

	if _, err := w.Write(full[:1]); err != nil {
		t.Fatalf("first Write error: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("after 1st Write, downstream got %q, want nothing", buf.Bytes())
	}
	if _, err := w.Write(full[1:]); err != nil {
		t.Fatalf("second Write error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), full) {
		t.Fatalf("buf = %v, want %v", buf.Bytes(), full)
	}
}

func TestRuneSafeWriter_ThreeByteChar_SplitAt2And1(t *testing.T) {
	full := []byte("あ")
	var buf bytes.Buffer
	w := newRuneSafeWriter(&buf)

	if _, err := w.Write(full[:2]); err != nil {
		t.Fatalf("first Write error: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("after 1st Write, downstream got %q, want nothing", buf.Bytes())
	}
	if _, err := w.Write(full[2:]); err != nil {
		t.Fatalf("second Write error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), full) {
		t.Fatalf("buf = %v, want %v", buf.Bytes(), full)
	}
}

func TestRuneSafeWriter_FourByteChar_AllSplitPatterns(t *testing.T) {
	full := []byte("😀") // F0 9F 98 80
	if len(full) != 4 {
		t.Fatalf("test fixture wrong length: %d", len(full))
	}
	splits := [][2]int{{1, 3}, {2, 2}, {3, 1}}
	for _, sp := range splits {
		var buf bytes.Buffer
		w := newRuneSafeWriter(&buf)
		first, second := sp[0], sp[1]
		if _, err := w.Write(full[:first]); err != nil {
			t.Fatalf("split %v: first Write error: %v", sp, err)
		}
		if buf.Len() != 0 {
			t.Fatalf("split %v: after 1st Write, downstream got %q, want nothing", sp, buf.Bytes())
		}
		if _, err := w.Write(full[first : first+second]); err != nil {
			t.Fatalf("split %v: second Write error: %v", sp, err)
		}
		if !bytes.Equal(buf.Bytes(), full) {
			t.Fatalf("split %v: buf = %v, want %v", sp, buf.Bytes(), full)
		}
	}
}

func TestRuneSafeWriter_TwoCompleteStrings_OneWrite(t *testing.T) {
	var buf bytes.Buffer
	w := newRuneSafeWriter(&buf)
	if _, err := w.Write([]byte("あい")); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if buf.String() != "あい" {
		t.Fatalf("buf = %q, want %q", buf.String(), "あい")
	}
	if len(w.buf) != 0 {
		t.Fatalf("w.buf = %v, want empty", w.buf)
	}
}

func TestRuneSafeWriter_Flush_WithPending(t *testing.T) {
	full := []byte("あ")
	var buf bytes.Buffer
	w := newRuneSafeWriter(&buf)
	if _, err := w.Write(full[:1]); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), full[:1]) {
		t.Fatalf("buf = %v, want %v (raw incomplete bytes)", buf.Bytes(), full[:1])
	}
	if len(w.buf) != 0 {
		t.Fatalf("w.buf after Flush = %v, want empty", w.buf)
	}
}

func TestRuneSafeWriter_Flush_NoPending_NoOp(t *testing.T) {
	var buf bytes.Buffer
	w := newRuneSafeWriter(&buf)
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("buf = %v, want empty (no-op)", buf.Bytes())
	}
}

func TestRuneSafeWriter_InvalidLeadByte_PassesThroughWithoutHanging(t *testing.T) {
	var buf bytes.Buffer
	w := newRuneSafeWriter(&buf)
	data := []byte{0xff}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("buf = %v, want %v (invalid lead byte written through)", buf.Bytes(), data)
	}
	if len(w.buf) != 0 {
		t.Fatalf("w.buf = %v, want empty", w.buf)
	}
}

type errWriter struct {
	err error
}

func (e *errWriter) Write(_ []byte) (int, error) {
	return 0, e.err
}

// spyWriter は Write が呼ばれた回数を記録する。downstream への Write 呼出しが
// 「complete が空のときはスキップされる」ことを検証するために使う
// (バイト列が空でも bytes.Buffer.Write は無害な no-op を返すため、
// 呼び出し自体が起きたかどうかは戻り値だけでは判定できない)
type spyWriter struct {
	bytes.Buffer
	calls int
}

func (s *spyWriter) Write(p []byte) (int, error) {
	s.calls++
	return s.Buffer.Write(p)
}

func TestRuneSafeWriter_NoDownstreamWriteWhenCompleteEmpty(t *testing.T) {
	// "あ" の先頭 1 バイトのみでは complete が空になるはずで、
	// downstream の Write は一度も呼ばれてはならない
	spy := &spyWriter{}
	w := newRuneSafeWriter(spy)
	if _, err := w.Write([]byte("あ")[:1]); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if spy.calls != 0 {
		t.Fatalf("downstream Write called %d times, want 0 (complete was empty)", spy.calls)
	}
}

func TestRuneSafeWriter_DownstreamWriteCalledWhenCompleteNonEmpty(t *testing.T) {
	spy := &spyWriter{}
	w := newRuneSafeWriter(spy)
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("downstream Write called %d times, want 1", spy.calls)
	}
}

func TestRuneSafeWriter_DownstreamWriteError(t *testing.T) {
	wantErr := errors.New("downstream broke")
	w := newRuneSafeWriter(&errWriter{err: wantErr})
	n, err := w.Write([]byte("hello"))
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if len(w.buf) != 0 {
		t.Fatalf("w.buf = %v, want empty after error (no resend)", w.buf)
	}
}

func TestRuneSafeWriter_DownstreamWriteError_WithCarriedOverPending(t *testing.T) {
	full := []byte("あ") // E3 81 82
	wantErr := errors.New("downstream broke")
	w := newRuneSafeWriter(nil)
	// prime with incomplete tail via a working buffer first
	var buf bytes.Buffer
	w.w = &buf
	if _, err := w.Write(full[:1]); err != nil {
		t.Fatalf("priming Write error: %v", err)
	}
	if len(w.buf) != 1 {
		t.Fatalf("expected pending 1 byte, got %v", w.buf)
	}

	w.w = &errWriter{err: wantErr}
	n, err := w.Write([]byte("x"))
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if len(w.buf) != 0 {
		t.Fatalf("w.buf = %v, want empty (both pending and new bytes lost)", w.buf)
	}
}

func TestRuneSafeWriter_ByteAtATime_LongASCII(t *testing.T) {
	input := "The quick brown fox jumps over the lazy dog. 0123456789."
	var buf bytes.Buffer
	w := newRuneSafeWriter(&buf)
	for i := 0; i < len(input); i++ {
		if _, err := w.Write([]byte{input[i]}); err != nil {
			t.Fatalf("Write error at byte %d: %v", i, err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if buf.String() != input {
		t.Fatalf("buf = %q, want %q", buf.String(), input)
	}
}

func TestRuneSafeWriter_QuickSplitPositions(t *testing.T) {
	input := "hello あ world 😀 テスト!"
	f := func(splitAt uint8) bool {
		n := len(input)
		pos := int(splitAt) % (n + 1)
		var buf bytes.Buffer
		w := newRuneSafeWriter(&buf)
		if _, err := w.Write([]byte(input[:pos])); err != nil {
			return false
		}
		if _, err := w.Write([]byte(input[pos:])); err != nil {
			return false
		}
		if err := w.Flush(); err != nil {
			return false
		}
		return buf.String() == input
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Error(err)
	}
}

func TestLeadByteLen(t *testing.T) {
	cases := []struct {
		b    byte
		want int
	}{
		{0x41, 1}, // 'A'
		{0x7f, 1}, // ASCII boundary
		{0xC2, 2}, // 2-byte lead
		{0xE3, 3}, // 3-byte lead ("あ")
		{0xF0, 4}, // 4-byte lead ("😀")
		{0x80, 0}, // continuation byte
		{0xBF, 0}, // continuation byte
		{0xFF, 0}, // invalid
		{0xF8, 0}, // invalid 5-byte pattern (not supported by UTF-8)
	}
	for _, c := range cases {
		if got := leadByteLen(c.b); got != c.want {
			t.Errorf("leadByteLen(0x%02X) = %d, want %d", c.b, got, c.want)
		}
	}
}

func TestSplitIncompleteTail_EmptyInput(t *testing.T) {
	complete, pending := splitIncompleteTail(nil)
	if len(complete) != 0 || len(pending) != 0 {
		t.Fatalf("got complete=%v pending=%v, want both empty", complete, pending)
	}
}

func TestSplitIncompleteTail_CompleteMultibyteAtEnd(t *testing.T) {
	data := []byte("hello あ")
	complete, pending := splitIncompleteTail(data)
	if !bytes.Equal(complete, data) {
		t.Fatalf("complete = %v, want %v", complete, data)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %v, want empty", pending)
	}
}

func TestSplitIncompleteTail_ValidUTF8AfterSplit(t *testing.T) {
	// Ensure the "complete" half is always valid UTF-8 for a variety of real strings.
	inputs := []string{"あ", "😀", "hello", "こんにちは世界", "🎉あ😀い"}
	for _, in := range inputs {
		b := []byte(in)
		for i := 0; i <= len(b); i++ {
			complete, _ := splitIncompleteTail(b[:i])
			if !utf8.Valid(complete) {
				t.Errorf("splitIncompleteTail(%q[:%d]) complete part not valid utf8: %v", in, i, complete)
			}
		}
	}
}
