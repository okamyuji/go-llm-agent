package cliui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/creack/pty"
)

// TestREPL_SetupEditor_TerminalBuildsEditor 入力が端末なら raw 化に成功し
// 行エディタが構築される
func TestREPL_SetupEditor_TerminalBuildsEditor(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open err=%v", err)
	}
	t.Cleanup(func() { _ = tty.Close(); _ = ptmx.Close() })

	var buf bytes.Buffer
	r := &REPL{in: tty, out: &buf}
	pump := newBytePump(strings.NewReader(""))
	out, editor, closeEditor := r.setupEditor(context.Background(), pump)
	defer closeEditor()
	if editor == nil {
		t.Fatal("端末では行エディタを構築する期待")
	}
	if out == nil {
		t.Fatal("出力先が nil")
	}
}

// TestREPL_NewEditor_AppliesTerminalSize 端末サイズの取得に成功したら
// 行エディタへ反映する。幅 10 桁では ">> " + 7 文字で右端に達し、
// 折返しの CRLF が 1 回追加される (既定の 80 桁では折返さない)
func TestREPL_NewEditor_AppliesTerminalSize(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open err=%v", err)
	}
	t.Cleanup(func() { _ = tty.Close(); _ = ptmx.Close() })
	if err := pty.Setsize(tty, &pty.Winsize{Rows: 24, Cols: 10}); err != nil {
		t.Fatalf("pty.Setsize err=%v", err)
	}

	var buf bytes.Buffer
	r := &REPL{in: tty, out: &buf}
	pump := newBytePump(strings.NewReader("abcdefg\r"))
	_, editor, closeEditor := r.setupEditor(context.Background(), pump)
	defer closeEditor()
	if editor == nil {
		t.Fatal("端末では行エディタを構築する期待")
	}
	line, err := editor.ReadLine()
	if err != nil {
		t.Fatalf("ReadLine err=%v", err)
	}
	if line != "abcdefg" {
		t.Fatalf("line=%q, want \"abcdefg\"", line)
	}
	if got := strings.Count(buf.String(), "\r\n"); got != 2 {
		t.Fatalf("CRLF=%d, want 2 (折返し 1 + 確定 1): %q", got, buf.String())
	}
}
