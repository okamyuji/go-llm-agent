package cliui_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

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

// scriptedSvc 指定したイベント列をそのまま流す
type scriptedSvc struct {
	events []agent.Event
}

func (s scriptedSvc) Run(_ context.Context, _ agent.Input, out chan<- agent.Event) error {
	for _, ev := range s.events {
		out <- ev
	}
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

// TestRepl_PrintsTurnSummary turn 終了時に done in / token サマリが出る。
// bytes.Buffer は非 TTY なのでスピナー描画は no-op、サマリ行だけ確認する
func TestRepl_PrintsTurnSummary(t *testing.T) {
	svc := scriptedSvc{events: []agent.Event{
		{Kind: agent.EventDelta, Delta: "hello"},
		{Kind: agent.EventUsage, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}},
		{Kind: agent.EventFinal, Final: &llm.Message{Role: llm.RoleAssistant, Content: "hello"}},
	}}
	in := strings.NewReader("hi\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("err=%v", err)
	}
	got := out.String()
	if !strings.Contains(got, "done in") {
		t.Errorf("expected 'done in' summary, got %q", got)
	}
	if !strings.Contains(got, "in 10 / out 5 tok") {
		t.Errorf("expected token counts, got %q", got)
	}
}

// TestRepl_DisableSpinner サマリ行も含めて完全に旧来出力（goroutine も起動しない）
func TestRepl_DisableSpinner(t *testing.T) {
	svc := scriptedSvc{events: []agent.Event{
		{Kind: agent.EventDelta, Delta: "hi"},
		{Kind: agent.EventFinal, Final: &llm.Message{Role: llm.RoleAssistant, Content: "hi"}},
	}}
	in := strings.NewReader("hi\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("err=%v", err)
	}
	got := out.String()
	if strings.Contains(got, "done in") {
		t.Errorf("did not expect summary line when DisableSpinner=true, got %q", got)
	}
	if !strings.Contains(got, "hi") {
		t.Errorf("expected delta in output, got %q", got)
	}
}

// TestRepl_ToolCallSummary ツール呼出が 1 回ある場合、サマリの tool 数が 1 になる
func TestRepl_ToolCallSummary(t *testing.T) {
	svc := scriptedSvc{events: []agent.Event{
		{Kind: agent.EventToolCall, ToolCall: &llm.ToolCall{ID: "1", Name: "fs_read"}},
		{Kind: agent.EventToolResult, ToolResult: &agent.ToolResult{CallID: "1", Name: "fs_read", Content: "ok"}},
		{Kind: agent.EventDelta, Delta: "done"},
		{Kind: agent.EventFinal, Final: &llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	}}
	in := strings.NewReader("hi\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("err=%v", err)
	}
	got := out.String()
	if !strings.Contains(got, "1 tool") {
		t.Errorf("expected '1 tool' in summary, got %q", got)
	}
}

// blockingSvc は delta を 1 つ出した後、context キャンセル (ESC 中断) まで待つ。
// 長時間生成をシミュレートし、ESC でそのターンだけ止まることを検証する。
type blockingSvc struct{}

func (blockingSvc) Run(ctx context.Context, _ agent.Input, out chan<- agent.Event) error {
	out <- agent.Event{Kind: agent.EventDelta, Delta: "生成中"}
	<-ctx.Done()
	return ctx.Err()
}

// TestRepl_EscCancelsTurn 生成中に ESC を送るとそのターンだけ中断し、
// セッションは継続して次の入力 (/quit) を処理できる。
func TestRepl_EscCancelsTurn(t *testing.T) {
	// "hi"+Enter で 1 ターン開始 → ESC で中断 → "/quit"+Enter で終了
	in := strings.NewReader("hi\n\x1b/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(blockingSvc{}, cliui.Options{Model: "test/m", In: in, Out: &out})
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not terminate — ESC likely did not cancel the turn")
	}
	got := out.String()
	if !strings.Contains(got, "中断しました") {
		t.Errorf("expected interrupt notice, got %q", got)
	}
	if strings.Contains(got, "[error]") {
		t.Errorf("ESC cancellation must not surface as an error, got %q", got)
	}
	if strings.Contains(got, "done in") {
		t.Errorf("turn summary must be suppressed after interruption, got %q", got)
	}
}

// TestRepl_CtrlCDuringGenerationQuits 生成中の Ctrl-C はそのターンを中断しセッションを終了する。
func TestRepl_CtrlCDuringGenerationQuits(t *testing.T) {
	in := strings.NewReader("hi\n\x03")
	var out bytes.Buffer
	r := cliui.NewREPL(blockingSvc{}, cliui.Options{Model: "test/m", In: in, Out: &out})
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not terminate on Ctrl-C during generation")
	}
	if strings.Contains(out.String(), "[error]") {
		t.Errorf("Ctrl-C must not surface as an error, got %q", out.String())
	}
}

// TestRepl_EscThenCtrlCQuits ESC と Ctrl-C が同一バッチで届いても Ctrl-C は失われず終了する。
func TestRepl_EscThenCtrlCQuits(t *testing.T) {
	in := strings.NewReader("hi\n\x1b\x03")
	var out bytes.Buffer
	r := cliui.NewREPL(blockingSvc{}, cliui.Options{Model: "test/m", In: in, Out: &out})
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not terminate — Ctrl-C after ESC was lost")
	}
}
