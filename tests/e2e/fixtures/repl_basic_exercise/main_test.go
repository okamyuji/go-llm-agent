package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplitRunes_MultiByteBoundary(t *testing.T) {
	got := splitRunes("あ😀いう", 2)
	want := []string{"あ😀", "いう"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want=%d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestSplitRunes_NonPositiveSizeReturnsWhole(t *testing.T) {
	got := splitRunes("あい", 0)
	if len(got) != 1 || got[0] != "あい" {
		t.Fatalf("got=%v", got)
	}
}

func TestSplitRunes_EmptyStringReturnsNothing(t *testing.T) {
	if got := splitRunes("", 3); len(got) != 0 {
		t.Fatalf("got=%v", got)
	}
}

func TestWriteSSE_EmitsDataLine(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := writeSSE(rec, sseChunk{Choices: []sseChoice{{Delta: sseDelta{Content: "あ"}}}}); err != nil {
		t.Fatalf("err=%v", err)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "data: {") || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("body=%q", body)
	}
	if !strings.Contains(body, `"content":"あ"`) {
		t.Fatalf("content が入っていない: %q", body)
	}
}

func TestCheckDeltaScenario_PassesAgainstStub(t *testing.T) {
	srv := newStubServer()
	defer srv.Close()
	var out bytes.Buffer
	if err := checkDeltaScenario(context.Background(), srv.URL, &out); err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(out.String(), "repl_delta_utf8_ok=true") {
		t.Fatalf("out=%q", out.String())
	}
}

func TestCheckDeltaScenario_UnreachableStubReturnsError(t *testing.T) {
	srv := newStubServer()
	srv.Close()
	var out bytes.Buffer
	if err := checkDeltaScenario(context.Background(), srv.URL, &out); err == nil {
		t.Fatal("到達不能な stub ではエラーを返すこと")
	}
}

func TestRun_AllKeysEmitted(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), &out); err != nil {
		t.Fatalf("run err=%v", err)
	}
	for _, key := range []string{
		"repl_delta_utf8_ok=true",
		"repl_help_ok=true",
		"repl_model_ok=true",
		"repl_cost_ok=true",
	} {
		if !strings.Contains(out.String(), key) {
			t.Fatalf("missing %q in %q", key, out.String())
		}
	}
}
