package cliui_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// TestMain パッケージ全テスト終了時に goroutine リークを検証する
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type fakeSvc struct{}

func (fakeSvc) Run(_ context.Context, _ agent.Input, out chan<- agent.Event) error {
	out <- agent.Event{Kind: agent.EventDelta, Delta: "hello"}
	final := llm.Message{Role: llm.RoleAssistant, Content: "hello"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final}
	return nil
}

func TestRepl_OneTurn(t *testing.T) {
	in := strings.NewReader("hi\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(fakeSvc{}, cliui.Options{Model: "fake/m", In: in, Out: &out})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("stream 表示なし: %q", out.String())
	}
}

func TestRunOneShot(t *testing.T) {
	var buf bytes.Buffer
	if err := cliui.RunOneShot(context.Background(), fakeSvc{}, "fake/m", "", "hi", 1, &buf); err != nil {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("output=%q", buf.String())
	}
}
