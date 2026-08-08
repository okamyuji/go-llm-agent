package cliui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadLineSimple(t *testing.T) {
	e := newLineEditor(strings.NewReader("hello\r"), &bytes.Buffer{}, ">> ")
	got, err := e.readLine()
	if err != nil {
		t.Fatalf("readLine err=%v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestReadLineJapanese(t *testing.T) {
	e := newLineEditor(strings.NewReader("こんにちは\r"), &bytes.Buffer{}, ">> ")
	got, err := e.readLine()
	if err != nil {
		t.Fatalf("readLine err=%v", err)
	}
	if got != "こんにちは" {
		t.Errorf("got %q, want こんにちは", got)
	}
}

func TestReadLineCursorInsertAndBackspace(t *testing.T) {
	// "abc" 入力後、Left Left で 'a' と 'b' の間へ、'X' 挿入 → "aXbc"
	e := newLineEditor(strings.NewReader("abc\x1b[D\x1b[DX\r"), &bytes.Buffer{}, ">> ")
	got, _ := e.readLine()
	if got != "aXbc" {
		t.Errorf("insert-at-cursor: got %q, want aXbc", got)
	}

	// "abc" + Backspace → "ab"
	e2 := newLineEditor(strings.NewReader("abc\x7f\r"), &bytes.Buffer{}, ">> ")
	got2, _ := e2.readLine()
	if got2 != "ab" {
		t.Errorf("backspace: got %q, want ab", got2)
	}
}

func TestReadLineCtrlCInterrupts(t *testing.T) {
	e := newLineEditor(strings.NewReader("partial\x03"), &bytes.Buffer{}, ">> ")
	_, err := e.readLine()
	if !errors.Is(err, errInterrupted) {
		t.Errorf("err=%v, want errInterrupted", err)
	}
}

func TestReadLineEOFOnEmpty(t *testing.T) {
	e := newLineEditor(strings.NewReader("\x04"), &bytes.Buffer{}, ">> ")
	_, err := e.readLine()
	if !errors.Is(err, io.EOF) {
		t.Errorf("err=%v, want io.EOF", err)
	}
}

func TestReadLineHistoryRecall(t *testing.T) {
	// 1 回目 "first" を確定 → 2 回目で Up を押すと "first" が復元され、そのまま確定
	in := strings.NewReader("first\r\x1b[A\r")
	out := &bytes.Buffer{}
	e := newLineEditor(in, out, ">> ")
	l1, _ := e.readLine()
	if l1 != "first" {
		t.Fatalf("first line = %q", l1)
	}
	l2, err := e.readLine()
	if err != nil {
		t.Fatalf("second readLine err=%v", err)
	}
	if l2 != "first" {
		t.Errorf("history recall: got %q, want first", l2)
	}
}

func TestReadLineHistoryUpDown(t *testing.T) {
	// 2 件履歴を作り、Up Up で最古、Down で新しい方へ戻れる
	e := newLineEditor(strings.NewReader("aaa\r"), &bytes.Buffer{}, ">> ")
	_, _ = e.readLine()
	e.in = newKeyReader(strings.NewReader("bbb\r"))
	_, _ = e.readLine()
	// Up(→bbb) Up(→aaa) Down(→bbb) で確定
	e.in = newKeyReader(strings.NewReader("\x1b[A\x1b[A\x1b[B\r"))
	got, _ := e.readLine()
	if got != "bbb" {
		t.Errorf("up-up-down: got %q, want bbb", got)
	}
}

func TestRenderContainsPromptAndText(t *testing.T) {
	out := &bytes.Buffer{}
	e := newLineEditor(strings.NewReader("hi\r"), out, ">> ")
	_, _ = e.readLine()
	s := out.String()
	if !strings.Contains(s, ">> ") {
		t.Errorf("output missing prompt: %q", s)
	}
	if !strings.Contains(s, "hi") {
		t.Errorf("output missing typed text: %q", s)
	}
}
