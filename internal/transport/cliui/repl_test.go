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

type historyCapturingSvc struct {
	inputs []agent.Input
}

func (s *historyCapturingSvc) Run(_ context.Context, in agent.Input, out chan<- agent.Event) error {
	s.inputs = append(s.inputs, in)
	if len(s.inputs) == 1 {
		turnMessages := []llm.Message{
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "search1", Name: "web_search"}}},
			{Role: llm.RoleTool, Name: "web_search", ToolCallID: "search1", Content: "search result"},
			{Role: llm.RoleAssistant, Content: "first answer"},
		}
		out <- agent.Event{Kind: agent.EventDelta, Delta: "first answer"}
		out <- agent.Event{Kind: agent.EventFinal, Final: &turnMessages[2], TurnMessages: turnMessages}
		return nil
	}
	final := llm.Message{Role: llm.RoleAssistant, Content: "more detail"}
	out <- agent.Event{Kind: agent.EventDelta, Delta: final.Content}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final, TurnMessages: []llm.Message{final}}
	return nil
}

type finalOnlyHistorySvc struct {
	inputs []agent.Input
}

func (s *finalOnlyHistorySvc) Run(_ context.Context, in agent.Input, out chan<- agent.Event) error {
	s.inputs = append(s.inputs, in)
	content := "first answer"
	if len(s.inputs) > 1 {
		content = "second answer"
	}
	final := llm.Message{Role: llm.RoleAssistant, Content: content}
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

func TestRepl_PreservesToolMessagesForFollowUp(t *testing.T) {
	svc := &historyCapturingSvc{}
	in := strings.NewReader("最新情報を調べて\nもう少し詳しく\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 2 {
		t.Fatalf("inputs=%d, want 2", len(svc.inputs))
	}
	got := svc.inputs[1].Messages
	if len(got) != 5 {
		t.Fatalf("second turn messages=%d, want 5: %+v", len(got), got)
	}
	if got[1].Role != llm.RoleAssistant || len(got[1].ToolCalls) != 1 {
		t.Errorf("messages[1]=%+v, want assistant tool call", got[1])
	}
	if got[2].Role != llm.RoleTool || got[2].Content != "search result" {
		t.Errorf("messages[2]=%+v, want tool result", got[2])
	}
	if got[3].Role != llm.RoleAssistant || got[3].Content != "first answer" {
		t.Errorf("messages[3]=%+v, want first final answer", got[3])
	}
	if got[4].Role != llm.RoleUser || got[4].Content != "もう少し詳しく" {
		t.Errorf("messages[4]=%+v, want follow-up user message", got[4])
	}
}

func TestRepl_PreservesFinalEventWithoutDeltaForFollowUp(t *testing.T) {
	svc := &finalOnlyHistorySvc{}
	in := strings.NewReader("first question\nfollow-up\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 2 {
		t.Fatalf("inputs=%d, want 2", len(svc.inputs))
	}
	messages := svc.inputs[1].Messages
	if len(messages) != 3 {
		t.Fatalf("second turn messages=%d, want 3: %+v", len(messages), messages)
	}
	if messages[1].Role != llm.RoleAssistant || messages[1].Content != "first answer" {
		t.Errorf("messages[1]=%+v, want EventFinal assistant payload", messages[1])
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

// interruptCapturingSvc 1 回目は EventFinal を出さずに context キャンセルまで待ち、
// 2 回目以降は通常応答する。中断後の履歴の形を検証するために Input を記録する。
type interruptCapturingSvc struct {
	inputs     []agent.Input
	firstDelta string // 空でなければ 1 回目の中断前に delta を流す
}

func (s *interruptCapturingSvc) Run(ctx context.Context, in agent.Input, out chan<- agent.Event) error {
	s.inputs = append(s.inputs, in)
	if len(s.inputs) == 1 {
		if s.firstDelta != "" {
			out <- agent.Event{Kind: agent.EventDelta, Delta: s.firstDelta}
		}
		<-ctx.Done()
		return ctx.Err()
	}
	final := llm.Message{Role: llm.RoleAssistant, Content: "second answer"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final, TurnMessages: []llm.Message{final}}
	return nil
}

// TestRepl_InterruptedEmptyTurnIsRolledBack 何も生成されないまま ESC 中断されたターンは
// user 入力ごと履歴から巻き戻す。空 content の assistant を積むと llama-server が
// 「content か tool_calls が必須」で 400 を返し、user 連続も交互制約で 400 になるため、
// どちらの形も次ターンの履歴に残してはならない。
func TestRepl_InterruptedEmptyTurnIsRolledBack(t *testing.T) {
	svc := &interruptCapturingSvc{}
	in := strings.NewReader("q1\n\x1bq2\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not terminate")
	}
	if len(svc.inputs) != 2 {
		t.Fatalf("inputs=%d, want 2", len(svc.inputs))
	}
	got := svc.inputs[1].Messages
	if len(got) != 1 || got[0].Role != llm.RoleUser || got[0].Content != "q2" {
		t.Fatalf("second turn messages=%+v, want only the new user message", got)
	}
}

// TestRepl_InterruptedPartialContentIsKept 部分生成テキストがあるまま中断されたターンは、
// その部分テキストを assistant として履歴に残す（content 非空なので履歴形状は有効）。
func TestRepl_InterruptedPartialContentIsKept(t *testing.T) {
	svc := &interruptCapturingSvc{firstDelta: "途中まで"}
	in := strings.NewReader("q1\n\x1bq2\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not terminate")
	}
	if len(svc.inputs) != 2 {
		t.Fatalf("inputs=%d, want 2", len(svc.inputs))
	}
	got := svc.inputs[1].Messages
	if len(got) != 3 {
		t.Fatalf("second turn messages=%+v, want [user q1, assistant partial, user q2]", got)
	}
	if got[1].Role != llm.RoleAssistant || got[1].Content != "途中まで" {
		t.Errorf("messages[1]=%+v, want partial assistant content", got[1])
	}
	if got[2].Role != llm.RoleUser || got[2].Content != "q2" {
		t.Errorf("messages[2]=%+v, want new user message", got[2])
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
