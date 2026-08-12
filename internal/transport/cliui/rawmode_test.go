package cliui

import (
	"bytes"
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

func TestBytePumpReadPromptCoalescesPastedLines(t *testing.T) {
	// ペーストで一括到着した複数行は 1 プロンプトに結合される
	p := newBytePump(strings.NewReader("line1\nline2\n\nline4\n"))
	got, err := p.readPrompt(200 * time.Millisecond)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != "line1\nline2\n\nline4" {
		t.Errorf("got %q, want joined multi-line prompt", got)
	}
}

func TestBytePumpReadPromptZeroWindowKeepsLineSemantics(t *testing.T) {
	// 窓 0 (パイプ入力) では従来どおり 1 行 = 1 プロンプト
	p := newBytePump(strings.NewReader("q1\nq2\n"))
	got, err := p.readPrompt(0)
	if err != nil || got != "q1" {
		t.Fatalf("got %q err=%v, want q1", got, err)
	}
	got, err = p.readPrompt(0)
	if err != nil || got != "q2" {
		t.Fatalf("got %q err=%v, want q2", got, err)
	}
}

func TestBytePumpReadPromptSeparatesLinesBeyondWindow(t *testing.T) {
	// 窓を超えて到着した行は別プロンプトになる（手入力の連続質問）
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	p := newBytePump(pr)
	go func() {
		_, _ = pw.Write([]byte("q1\n"))
		time.Sleep(150 * time.Millisecond)
		_, _ = pw.Write([]byte("q2\n"))
	}()
	got, err := p.readPrompt(30 * time.Millisecond)
	if err != nil || got != "q1" {
		t.Fatalf("got %q err=%v, want q1 (separate prompt)", got, err)
	}
	got, err = p.readPrompt(30 * time.Millisecond)
	if err != nil || got != "q2" {
		t.Fatalf("got %q err=%v, want q2", got, err)
	}
}

func TestBytePumpReadPromptRestoresPartialTailToPending(t *testing.T) {
	// 窓内に改行まで届かなかった末尾バイトは消費せず次の行読みへ戻す
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	p := newBytePump(pr)
	go func() { _, _ = pw.Write([]byte("x\ny")) }()
	got, err := p.readPrompt(50 * time.Millisecond)
	if err != nil || got != "x" {
		t.Fatalf("got %q err=%v, want x", got, err)
	}
	go func() { _, _ = pw.Write([]byte("z\n")) }()
	got, err = p.readPrompt(50 * time.Millisecond)
	if err != nil || got != "yz" {
		t.Fatalf("got %q err=%v, want yz (partial tail preserved)", got, err)
	}
}

func TestBytePumpReadPromptCtrlCInBurst(t *testing.T) {
	p := newBytePump(strings.NewReader("q1\n\x03"))
	_, err := p.readPrompt(200 * time.Millisecond)
	if !errors.Is(err, errCtrlC) {
		t.Fatalf("want errCtrlC, got %v", err)
	}
}

func TestBytePumpReadPromptConsumesPushbackLines(t *testing.T) {
	// 生成中に pushback された複数行も 1 プロンプトへ結合される
	p := newBytePump(strings.NewReader(""))
	for _, b := range []byte("one\ntwo\n") {
		p.pushback(b)
	}
	got, err := p.readPrompt(50 * time.Millisecond)
	if err != nil || got != "one\ntwo" {
		t.Fatalf("got %q err=%v, want one\\ntwo", got, err)
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
