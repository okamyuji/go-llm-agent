package cliui

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/term"
)

type fakeTermConn struct {
	io.Reader
	io.Writer
}

func newEditorForTest(input string) *term.Terminal {
	return term.NewTerminal(fakeTermConn{strings.NewReader(input), &bytes.Buffer{}}, ">> ")
}

func TestReadEditorPromptSingleLine(t *testing.T) {
	got, err := readEditorPrompt(newEditorForTest("こんにちは\r"))
	if err != nil || got != "こんにちは" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestReadEditorPromptJoinsBracketedPasteLines(t *testing.T) {
	// bracketed paste 内の複数行は 1 プロンプトに結合され、貼り付け後の Enter で確定する
	input := "\x1b[200~line1\nline2\nline3\x1b[201~\r"
	got, err := readEditorPrompt(newEditorForTest(input))
	if err != nil || got != "line1\nline2\nline3" {
		t.Fatalf("got %q err=%v, want joined 3 lines", got, err)
	}
}

func TestReadEditorPromptPasteWithTrailingNewlineWaitsForEnter(t *testing.T) {
	// 末尾改行付きの貼り付けは Enter で空行が確定し、全体が 1 プロンプトになる
	input := "\x1b[200~line1\nline2\n\x1b[201~\r"
	got, err := readEditorPrompt(newEditorForTest(input))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if strings.TrimSpace(got) != "line1\nline2" {
		t.Fatalf("got %q, want line1\\nline2 (after trim)", got)
	}
}

func TestReadEditorPromptPasteThenEditThenEnter(t *testing.T) {
	// 末尾改行なしの貼り付けは最終行が編集バッファに残り、追記して Enter で確定できる
	input := "\x1b[200~line1\nline2\x1b[201~続き\r"
	got, err := readEditorPrompt(newEditorForTest(input))
	if err != nil || got != "line1\nline2続き" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestReadEditorPromptCtrlCIsEOF(t *testing.T) {
	_, err := readEditorPrompt(newEditorForTest("\x03"))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("want io.EOF on Ctrl-C, got %v", err)
	}
}
