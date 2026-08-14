package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newPipeSession waitFor / sendAndWait を検証するための擬似セッションを作る
func newPipeSession(t *testing.T) (*session, *os.File) {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe err=%v", err)
	}
	s := &session{f: pw, out: &safeBuf{}}
	t.Cleanup(func() { _ = pr.Close(); _ = pw.Close() })
	return s, pr
}

func TestSession_WaitFor_AdvancesCursorPastMatch(t *testing.T) {
	s := &session{out: &safeBuf{}}
	if _, err := s.out.Write([]byte("abcXYZdef")); err != nil {
		t.Fatalf("err=%v", err)
	}
	got, err := s.waitFor("XYZ")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != "abcXYZ" {
		t.Fatalf("got=%q", got)
	}
	if _, err := s.waitFor("def"); err != nil {
		t.Fatalf("cursor 以降を探せていない: %v", err)
	}
}

func TestSession_WaitFor_TimesOutWhenAbsent(t *testing.T) {
	saved := waitTimeout
	waitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { waitTimeout = saved })

	s := &session{out: &safeBuf{}}
	if _, err := s.out.Write([]byte("abc")); err != nil {
		t.Fatalf("err=%v", err)
	}
	if _, err := s.waitFor("XYZ"); err == nil {
		t.Fatal("出現しない文字列でエラーを返すこと")
	}
}

func TestSession_SendAndWait_WritesToPTY(t *testing.T) {
	s, pr := newPipeSession(t)
	go func() {
		b := make([]byte, 16)
		n, _ := pr.Read(b)
		_, _ = s.out.Write(b[:n])
	}()
	got, err := s.sendAndWait("ping", "ping")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != "ping" {
		t.Fatalf("got=%q", got)
	}
}

func TestTail(t *testing.T) {
	if got := tail("あいう", 10); got != "あいう" {
		t.Fatalf("got=%q", got)
	}
	if got := tail("あいうえお", 2); got != "えお" {
		t.Fatalf("got=%q", got)
	}
}

func TestWriteConfig_EmbedsBaseURL(t *testing.T) {
	dir := t.TempDir()
	if err := writeConfig(dir, "http://127.0.0.1:9999"); err != nil {
		t.Fatalf("err=%v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	body := string(b)
	for _, want := range []string{"llamacpp/stub", "http://127.0.0.1:9999/v1", "allow_models", dir} {
		if !strings.Contains(body, want) {
			t.Fatalf("config に %q が無い: %s", want, body)
		}
	}
}

func TestWriteHistory_ContainsBothEntries(t *testing.T) {
	dir := t.TempDir()
	if err := writeHistory(dir); err != nil {
		t.Fatalf("err=%v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".agent_history"))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	for _, want := range []string{historyEntry, olderHistoryEntry} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("履歴に %q が無い: %s", want, b)
		}
	}
}

func TestAsNoPTY(t *testing.T) {
	var target errNoPTY
	if !asNoPTY(errNoPTY{err: errors.New("boom")}, &target) {
		t.Fatal("errNoPTY を判定できていない")
	}
	if !strings.Contains(target.Error(), "boom") {
		t.Fatalf("Error()=%q", target.Error())
	}
	if asNoPTY(errors.New("other"), &target) {
		t.Fatal("無関係なエラーで true")
	}
}

func TestSkipIfNoPTY(t *testing.T) {
	var out bytes.Buffer
	if err := skipIfNoPTY(errNoPTY{err: errors.New("boom")}, &out); err != nil {
		t.Fatalf("pty 不可は skip 扱い: %v", err)
	}
	if !strings.Contains(out.String(), "tty_skipped=true") {
		t.Fatalf("out=%q", out.String())
	}
	out.Reset()
	if err := skipIfNoPTY(nil, &out); err != nil {
		t.Fatalf("err=%v", err)
	}
	if out.String() != "" {
		t.Fatalf("成功時に出力した: %q", out.String())
	}
	if err := skipIfNoPTY(errors.New("other"), &out); err == nil {
		t.Fatal("無関係なエラーはそのまま返すこと")
	}
}

func TestStub_LastUserMessage(t *testing.T) {
	s := &stub{}
	if got := s.lastUserMessage(); got != "" {
		t.Fatalf("got=%q", got)
	}
	s.userMsgs = []string{"a", "b"}
	if got := s.lastUserMessage(); got != "b" {
		t.Fatalf("got=%q", got)
	}
}

func TestRun_AllKeysEmitted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	err := run(ctx, t.TempDir(), &out)
	var noPTY errNoPTY
	if err != nil && asNoPTY(err, &noPTY) {
		t.Skip("擬似端末を割り当てられない環境のため skip")
	}
	if err != nil {
		t.Fatalf("run err=%v (out=%s)", err, out.String())
	}
	for _, key := range []string{
		"tty_bracketed_paste_on=true",
		"tty_prompt_shown=true",
		"tty_cursor_left_cjk=true",
		"tty_backspace_erases_cells=true",
		"tty_wrap_pads_cjk=true",
		"tty_search_prompt=true",
		"tty_search_candidate=true",
		"tty_search_older_candidate=true",
		"tty_search_abort_restores=true",
		"tty_search_submits_candidate=true",
		"tty_bracketed_paste_off=true",
	} {
		if !strings.Contains(out.String(), key) {
			t.Fatalf("missing %q in %q", key, out.String())
		}
	}
}
