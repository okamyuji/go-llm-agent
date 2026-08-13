package cliui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestBytePumpReadLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"LF terminated", "こんにちは\n", "こんにちは"},
		{"CRLF terminated", "hello\r\n", "hello"},
		{"EOF without newline", "partial", "partial"},
		{"empty line", "\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newBytePump(strings.NewReader(tt.input)).readLine()
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBytePumpReadLineEOFOnEmpty(t *testing.T) {
	_, err := newBytePump(strings.NewReader("")).readLine()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestBytePumpReadsMultipleLines(t *testing.T) {
	p := newBytePump(strings.NewReader("first\nsecond\n"))
	got, err := p.readLine()
	if err != nil || got != "first" {
		t.Fatalf("got %q err=%v", got, err)
	}
	got, err = p.readLine()
	if err != nil || got != "second" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestBytePumpPushback(t *testing.T) {
	// 生成中に消費されたバイトが次の readLine の先頭へ戻る
	p := newBytePump(strings.NewReader("cd\n"))
	p.pushback('a')
	p.pushback('b')
	got, err := p.readLine()
	if err != nil || got != "abcd" {
		t.Fatalf("got %q err=%v, want abcd", got, err)
	}
}

func TestBytePumpPushbackWithNewline(t *testing.T) {
	// pushback だけで行が完結する場合、チャネルを読まずに返り、残りは次行へ持ち越す
	p := newBytePump(strings.NewReader("")) // 入力なし
	for _, b := range []byte("one\ntwo\n") {
		p.pushback(b)
	}
	got, err := p.readLine()
	if err != nil || got != "one" {
		t.Fatalf("got %q err=%v, want one", got, err)
	}
	got, err = p.readLine()
	if err != nil || got != "two" {
		t.Fatalf("got %q err=%v, want two", got, err)
	}
}

func TestBytePumpReadLineCtrlC(t *testing.T) {
	// ターン終了直後に届いた Ctrl-C は行データではなく終了要求として扱う
	_, err := newBytePump(strings.NewReader("ab\x03")).readLine()
	if !errors.Is(err, errCtrlC) {
		t.Fatalf("want errCtrlC, got %v", err)
	}
}

func TestBytePumpReadLineCtrlCInPending(t *testing.T) {
	p := newBytePump(strings.NewReader(""))
	p.pushback('a')
	p.pushback(0x03)
	_, err := p.readLine()
	if !errors.Is(err, errCtrlC) {
		t.Fatalf("want errCtrlC, got %v", err)
	}
}

func TestBytePumpReadPromptLineSemantics(t *testing.T) {
	// パイプ入力は 1 行 = 1 プロンプト
	p := newBytePump(strings.NewReader("q1\nq2\n"))
	got, err := p.readPrompt(context.Background())
	if err != nil || got != "q1" {
		t.Fatalf("got %q err=%v, want q1", got, err)
	}
	got, err = p.readPrompt(context.Background())
	if err != nil || got != "q2" {
		t.Fatalf("got %q err=%v, want q2", got, err)
	}
}

func TestBytePumpReadPromptCtrlC(t *testing.T) {
	p := newBytePump(strings.NewReader("q1\n\x03"))
	if got, err := p.readPrompt(context.Background()); err != nil || got != "q1" {
		t.Fatalf("got %q err=%v, want q1", got, err)
	}
	_, err := p.readPrompt(context.Background())
	if !errors.Is(err, errCtrlC) {
		t.Fatalf("want errCtrlC, got %v", err)
	}
}

func TestBytePumpReadPromptConsumesPushbackFirst(t *testing.T) {
	// 生成中に pushback されたバイトは次のプロンプトの先頭で消費される
	p := newBytePump(strings.NewReader(""))
	for _, b := range []byte("one\n") {
		p.pushback(b)
	}
	got, err := p.readPrompt(context.Background())
	if err != nil || got != "one" {
		t.Fatalf("got %q err=%v, want one", got, err)
	}
}

func TestBytePumpReadPromptReturnsOnContextCancel(t *testing.T) {
	// プロンプト待ちでブロック中でも ctx キャンセル (SIGINT) で即座に抜ける
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	p := newBytePump(pr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.readPrompt(ctx)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readPrompt did not return on context cancel")
	}
}

func TestCRLFWriterTranslatesLoneLF(t *testing.T) {
	var buf bytes.Buffer
	w := newCRLFWriter(&buf)
	n, err := w.Write([]byte("a\nb"))
	if err != nil {
		t.Fatalf("write err=%v", err)
	}
	if n != 3 {
		t.Errorf("n=%d, want 3 (original length)", n)
	}
	if buf.String() != "a\r\nb" {
		t.Errorf("got %q, want a\\r\\nb", buf.String())
	}
}

func TestCRLFWriterDoesNotDoubleCRLF(t *testing.T) {
	var buf bytes.Buffer
	w := newCRLFWriter(&buf)
	_, _ = w.Write([]byte("a\r\nb"))
	if buf.String() != "a\r\nb" {
		t.Errorf("got %q, want a\\r\\nb (no extra CR)", buf.String())
	}
}

func TestCRLFWriterCRLFSplitAcrossWrites(t *testing.T) {
	var buf bytes.Buffer
	w := newCRLFWriter(&buf)
	_, _ = w.Write([]byte("x\r"))
	_, _ = w.Write([]byte("\ny"))
	if buf.String() != "x\r\ny" {
		t.Errorf("got %q, want x\\r\\ny", buf.String())
	}
}

func TestBytePump_ReadAnswerLine_ConsumesPendingFirst(t *testing.T) {
	p := newBytePump(strings.NewReader("second\n"))
	for _, b := range []byte("y\n") {
		p.pushback(b)
	}
	got, err := p.readAnswerLine(context.Background())
	if err != nil || got != "y" {
		t.Fatalf("pushback 済みを先に消費する期待 got %q err=%v", got, err)
	}
	got, err = p.readAnswerLine(context.Background())
	if err != nil || got != "second" {
		t.Fatalf("残りは channel から読む期待 got %q err=%v", got, err)
	}
}

func TestBytePump_ReadAnswerLine_CtrlC(t *testing.T) {
	p := newBytePump(strings.NewReader("\x03"))
	if _, err := p.readAnswerLine(context.Background()); !errors.Is(err, errCtrlC) {
		t.Fatalf("errCtrlC 期待 got %v", err)
	}
}

func TestBytePump_ReadAnswerLine_CtrlCInPending(t *testing.T) {
	p := newBytePump(strings.NewReader(""))
	for _, b := range []byte("ab\x03") {
		p.pushback(b)
	}
	if _, err := p.readAnswerLine(context.Background()); !errors.Is(err, errCtrlC) {
		t.Fatalf("pushback 途中の Ctrl-C も検出する期待 got %v", err)
	}
}

func TestBytePump_ReadAnswerLine_ESC(t *testing.T) {
	p := newBytePump(strings.NewReader("\x1b"))
	if _, err := p.readAnswerLine(context.Background()); !errors.Is(err, errESC) {
		t.Fatalf("errESC 期待 got %v", err)
	}
}

func TestBytePump_ReadAnswerLine_ESCInPending(t *testing.T) {
	p := newBytePump(strings.NewReader(""))
	for _, b := range []byte("ab\x1b") {
		p.pushback(b)
	}
	if _, err := p.readAnswerLine(context.Background()); !errors.Is(err, errESC) {
		t.Fatalf("pushback 途中の ESC も検出する期待 got %v", err)
	}
}

func TestBytePump_ReadAnswerLine_EOF(t *testing.T) {
	p := newBytePump(strings.NewReader(""))
	if _, err := p.readAnswerLine(context.Background()); err == nil {
		t.Fatal("入力終端でエラー期待")
	}
	p2 := newBytePump(strings.NewReader("no newline"))
	got, err := p2.readAnswerLine(context.Background())
	if err != nil || got != "no newline" {
		t.Fatalf("改行なしの残りは 1 行として返る期待 got %q err=%v", got, err)
	}
}

func TestBytePump_ReadAnswerLine_CtxDone(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	p := newBytePump(pr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.readAnswerLine(ctx)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("context.Canceled 期待 got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readAnswerLine が ctx キャンセルで返らない")
	}
}
