package cliui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// failingReader 1 度だけ読ませたあと非 EOF のエラーを返す io.Reader
type failingReader struct {
	head string
	err  error
	done bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.done && r.head != "" {
		n := copy(p, r.head)
		r.head = r.head[n:]
		return n, nil
	}
	r.done = true
	return 0, r.err
}

// TestBytePump_ReadAnswerLine_ReportsReaderError 入力が非 EOF のエラーで尽きた場合、
// io.EOF ではなく元のエラーを返す。端末の切断とファイル終端を呼び出し元が
// 区別できる必要がある
func TestBytePump_ReadAnswerLine_ReportsReaderError(t *testing.T) {
	sentinel := errors.New("read device failure")
	p := newBytePump(&failingReader{err: sentinel})
	got, err := p.readAnswerLine(context.Background())
	if got != "" {
		t.Errorf("line=%q, want 空文字列", got)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("err=%v, want %v", err, sentinel)
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("err=%v, want io.EOF ではない", err)
	}
}

// TestBytePump_ReadAnswerLine_ErrorAfterPartialLineReturnsLine 改行なしで
// エラー終端した場合は、読めた分を 1 行として返しエラーは伏せる
func TestBytePump_ReadAnswerLine_ErrorAfterPartialLineReturnsLine(t *testing.T) {
	p := newBytePump(&failingReader{head: "yes", err: errors.New("boom")})
	got, err := p.readAnswerLine(context.Background())
	if err != nil {
		t.Fatalf("err=%v, want nil", err)
	}
	if got != "yes" {
		t.Fatalf("line=%q, want \"yes\"", got)
	}
}

// TestBytePump_ReadAnswerLine_EOFReaderReturnsEOF strings.Reader は io.EOF で
// 終端するため、そのまま io.EOF が返る
func TestBytePump_ReadAnswerLine_EOFReaderReturnsEOF(t *testing.T) {
	p := newBytePump(strings.NewReader(""))
	if _, err := p.readAnswerLine(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("err=%v, want io.EOF", err)
	}
}
