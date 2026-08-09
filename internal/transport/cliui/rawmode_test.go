package cliui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadInputLine(t *testing.T) {
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
			got, err := readInputLine(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadInputLineEOFOnEmpty(t *testing.T) {
	_, err := readInputLine(strings.NewReader(""))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestReadInputLineReadsExactlyOneLine(t *testing.T) {
	// bufio と違い先読みしないので、次行は Reader に残る
	r := strings.NewReader("first\nsecond\n")
	got, err := readInputLine(r)
	if err != nil || got != "first" {
		t.Fatalf("got %q err=%v", got, err)
	}
	got, err = readInputLine(r)
	if err != nil || got != "second" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestScanForCancel(t *testing.T) {
	var esc, ctrlC int
	// 通常文字は無視され、ESC と Ctrl-C だけ検出される
	scanForCancel(strings.NewReader("ab\x1bcd\x03\x1b"),
		func() { esc++ }, func() { ctrlC++ })
	if esc != 2 {
		t.Errorf("esc=%d, want 2", esc)
	}
	if ctrlC != 1 {
		t.Errorf("ctrlC=%d, want 1", ctrlC)
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
