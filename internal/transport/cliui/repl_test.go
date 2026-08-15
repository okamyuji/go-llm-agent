package cliui_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
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
	// "· 1 tool ·" とデリミタごと確認する。"1 tool" だけだと toolCount が誤って
	// -1 になった場合の "-1 tool" もこの部分文字列を含んでしまい判別できない
	if !strings.Contains(got, "· 1 tool ·") {
		t.Errorf("expected '· 1 tool ·' in summary, got %q", got)
	}
	if !strings.Contains(got, "[tool_call fs_read]") {
		t.Errorf("expected tool_call name in output, got %q", got)
	}
	if !strings.Contains(got, "[tool_result fs_read]") {
		t.Errorf("expected tool_result name in output, got %q", got)
	}
}

// TestRepl_ToolEventsWithNilPayloadDoNotPanicAndOmitName
// EventToolCall / EventToolResult の ToolCall / ToolResult が nil のとき、
// 名前欄が空のまま panic せず出力されることを確認する
func TestRepl_ToolEventsWithNilPayloadDoNotPanicAndOmitName(t *testing.T) {
	svc := scriptedSvc{events: []agent.Event{
		{Kind: agent.EventToolCall, ToolCall: nil},
		{Kind: agent.EventToolResult, ToolResult: nil},
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
	if !strings.Contains(got, "[tool_call ]") {
		t.Errorf("expected empty tool_call name, got %q", got)
	}
	if !strings.Contains(got, "[tool_result ]") {
		t.Errorf("expected empty tool_result name, got %q", got)
	}
}

// ctxCancelingSvc は Run 呼出し直後に呼び出し元から渡された cancel で
// 「Run に渡した ctx」自身 (root ctx) をキャンセルしてから EventError を送る。
// SIGINT による root context キャンセル (ESC/Ctrl-C 中断とは別経路) を模倣する
type ctxCancelingSvc struct {
	cancel context.CancelFunc
}

func (s ctxCancelingSvc) Run(_ context.Context, _ agent.Input, out chan<- agent.Event) error {
	s.cancel()
	out <- agent.Event{Kind: agent.EventError, Err: context.Canceled}
	return nil
}

// TestRepl_RootContextCancellationSuppressesErrorDisplay ESC/Ctrl-C を経由しない
// root context キャンセル (SIGINT 相当) でも [error] 表示が抑制されることを確認する
func TestRepl_RootContextCancellationSuppressesErrorDisplay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	svc := ctxCancelingSvc{cancel: cancel}
	in := strings.NewReader("hi\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out})
	_ = r.Run(ctx)
	got := out.String()
	if strings.Contains(got, "[error]") {
		t.Errorf("root ctx cancellation should suppress [error] display, got %q", got)
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

// inputCapturingSvc Input を記録して固定回答を返す
type inputCapturingSvc struct {
	inputs []agent.Input
}

func (s *inputCapturingSvc) Run(_ context.Context, in agent.Input, out chan<- agent.Event) error {
	s.inputs = append(s.inputs, in)
	final := llm.Message{Role: llm.RoleAssistant, Content: "answer"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final, TurnMessages: []llm.Message{final}}
	return nil
}

// TestRepl_ToolsCommandTogglesToolChoice /tools off でツール広告を止め (tool_choice none)、
// /tools on で戻す。小型ローカルモデルはツール定義があると長文の指示追従を失うため、
// 純粋な対話セッションでツールを切る手段として使う。
func TestRepl_ToolsCommandTogglesToolChoice(t *testing.T) {
	svc := &inputCapturingSvc{}
	in := strings.NewReader("q1\n/tools off\nq2\n/tools on\nq3\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 3 {
		t.Fatalf("inputs=%d, want 3 (/tools 行はターンを消費しない)", len(svc.inputs))
	}
	if svc.inputs[0].ToolChoice != nil {
		t.Errorf("q1 ToolChoice=%+v, want nil (既定)", svc.inputs[0].ToolChoice)
	}
	if svc.inputs[1].ToolChoice == nil || svc.inputs[1].ToolChoice.Mode != "none" {
		t.Errorf("q2 ToolChoice=%+v, want mode none after /tools off", svc.inputs[1].ToolChoice)
	}
	if svc.inputs[2].ToolChoice != nil {
		t.Errorf("q3 ToolChoice=%+v, want nil after /tools on", svc.inputs[2].ToolChoice)
	}
	if !strings.Contains(out.String(), "[tools]") {
		t.Errorf("state feedback missing: %q", out.String())
	}
}

// TestRepl_UnknownSlashCommandIsNotSentToLLM /tool on のようなタイプミスを LLM へ送ると、
// 架空のツール実行計画テキストが履歴に残って以後の回答が汚染されるため、
// "/" で始まる未知の入力はコマンド一覧を表示してターンを消費しない。
func TestRepl_UnknownSlashCommandIsNotSentToLLM(t *testing.T) {
	svc := &inputCapturingSvc{}
	in := strings.NewReader("/tool xyz\n/unknown\nq1\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 1 {
		t.Fatalf("inputs=%d, want 1 (slash 入力は LLM へ送らない)", len(svc.inputs))
	}
	if got := svc.inputs[0].Messages[len(svc.inputs[0].Messages)-1].Content; got != "q1" {
		t.Errorf("sent prompt=%q, want q1", got)
	}
	if !strings.Contains(out.String(), "/tools") || !strings.Contains(out.String(), "/clear") {
		t.Errorf("command help missing: %q", out.String())
	}
}

// TestRepl_ToolAliasWorks /tool は /tools の別名として受け付ける (押し間違い対策)
func TestRepl_ToolAliasWorks(t *testing.T) {
	svc := &inputCapturingSvc{}
	in := strings.NewReader("/tool off\nq1\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 1 || svc.inputs[0].ToolChoice == nil || svc.inputs[0].ToolChoice.Mode != "none" {
		t.Fatalf("inputs=%+v, want one input with tool_choice none", svc.inputs)
	}
}

// TestRepl_ClearCommandResetsHistory /clear は会話履歴を破棄する。
// 汚染された履歴 (架空のツール計画テキスト等) からセッション再起動なしで復旧する手段。
func TestRepl_ClearCommandResetsHistory(t *testing.T) {
	svc := &inputCapturingSvc{}
	in := strings.NewReader("q1\n/clear\nq2\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 2 {
		t.Fatalf("inputs=%d, want 2", len(svc.inputs))
	}
	got := svc.inputs[1].Messages
	if len(got) != 1 || got[0].Content != "q2" {
		t.Errorf("second turn messages=%+v, want only q2 after /clear", got)
	}
	if !strings.Contains(out.String(), "履歴") {
		t.Errorf("clear feedback missing: %q", out.String())
	}
}

// TestRepl_SessionsDirRecordsUserAndAssistant SessionsDir を設定すると 1 ターンで
// user/assistant の 2 行が JSONL に記録される
func TestRepl_SessionsDirRecordsUserAndAssistant(t *testing.T) {
	svc := &inputCapturingSvc{}
	dir := t.TempDir()
	in := strings.NewReader("q1\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true, SessionsDir: dir})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir err=%v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 session file, got %d", len(entries))
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile err=%v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 recorded lines (user+assistant), got %d: %q", len(lines), string(b))
	}
}

// TestRepl_SessionsDirUnsetRecordsNothing SessionsDir 未設定 (既存デフォルト) では
// ファイルが一切作られない
func TestRepl_SessionsDirUnsetRecordsNothing(t *testing.T) {
	svc := &inputCapturingSvc{}
	dir := t.TempDir()
	in := strings.NewReader("q1\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir err=%v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("want no files created, got %d", len(entries))
	}
}

// TestRepl_InitialHistoryIsSeenByFirstTurn InitialHistory に設定したメッセージが
// 最初のターンで agent.Input.Messages の先頭に含まれる
func TestRepl_InitialHistoryIsSeenByFirstTurn(t *testing.T) {
	svc := &inputCapturingSvc{}
	initial := []llm.Message{
		{Role: llm.RoleUser, Content: "past1"},
		{Role: llm.RoleAssistant, Content: "past2"},
	}
	in := strings.NewReader("q1\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true, InitialHistory: initial})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 1 {
		t.Fatalf("inputs=%d, want 1", len(svc.inputs))
	}
	got := svc.inputs[0].Messages
	if len(got) < 3 || got[0].Content != "past1" || got[1].Content != "past2" {
		t.Fatalf("Messages=%+v, want InitialHistory を先頭に含む", got)
	}
}

// TestRepl_SessionIDIsPassedToAgentInput SessionID を明示指定すると
// agent.Input.SessionID がその値と一致する
func TestRepl_SessionIDIsPassedToAgentInput(t *testing.T) {
	svc := &inputCapturingSvc{}
	in := strings.NewReader("q1\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true, SessionID: "fixed-session-id"})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 1 || svc.inputs[0].SessionID != "fixed-session-id" {
		t.Fatalf("inputs=%+v", svc.inputs)
	}
}

// TestRepl_ClearRotatesSessionFile /clear 後の新ターンは rotate 後の新しいファイルにのみ
// 記録され、rotate 前のファイルの内容は変わらない
func TestRepl_ClearRotatesSessionFile(t *testing.T) {
	svc := &inputCapturingSvc{}
	dir := t.TempDir()
	in := strings.NewReader("q1\n/clear\nq2\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true, SessionsDir: dir})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir err=%v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 session files after /clear rotate, got %d", len(entries))
	}
	for _, e := range entries {
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("ReadFile err=%v", rerr)
		}
		content := string(b)
		if strings.Contains(content, "q1") && strings.Contains(content, "q2") {
			t.Fatalf("file %s contains both turns; rotate should isolate them: %q", e.Name(), content)
		}
	}
}

// TestRepl_ClearDiscardsInitialHistoryFromRecording /clear は InitialHistory 由来の
// メッセージも破棄する。以後のターンで agent.Input.Messages にそれらが含まれない
func TestRepl_ClearDiscardsInitialHistoryFromRecording(t *testing.T) {
	svc := &inputCapturingSvc{}
	initial := []llm.Message{{Role: llm.RoleUser, Content: "past1"}, {Role: llm.RoleAssistant, Content: "past2"}}
	in := strings.NewReader("/clear\nq1\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true, InitialHistory: initial})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 1 {
		t.Fatalf("inputs=%d, want 1", len(svc.inputs))
	}
	for _, m := range svc.inputs[0].Messages {
		if m.Content == "past1" || m.Content == "past2" {
			t.Fatalf("InitialHistory leaked after /clear: %+v", svc.inputs[0].Messages)
		}
	}
}

// TestRepl_InterruptedTurnLeavesNoUserLineInSessionFile ターンが中断され
// turnMessages が空になる場合、JSONL に user 行が 1 件も残らない
func TestRepl_InterruptedTurnLeavesNoUserLineInSessionFile(t *testing.T) {
	svc := scriptedSvc{events: []agent.Event{
		{Kind: agent.EventError, Err: context.Canceled},
	}}
	dir := t.TempDir()
	in := strings.NewReader("q1\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true, SessionsDir: dir})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir err=%v", err)
	}
	for _, e := range entries {
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("ReadFile err=%v", rerr)
		}
		if strings.Contains(string(b), "q1") {
			t.Fatalf("中断されたターンの user 行が記録されている: %s", string(b))
		}
	}
}

// TestRepl_SessionWriteFailureShowsErrorAndContinues 書込み不可なディレクトリでは
// ターンは正常に完了し、出力に記録失敗のエラーが含まれる (Unix 環境限定)
func TestRepl_SessionWriteFailureShowsErrorAndContinues(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root では chmod 0o000 が効かない")
	}
	svc := &inputCapturingSvc{}
	parent := t.TempDir()
	dir := filepath.Join(parent, "readonly")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("mkdir err=%v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	sessionsDir := filepath.Join(dir, "sub")
	in := strings.NewReader("q1\n/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true, SessionsDir: sessionsDir})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if len(svc.inputs) != 1 {
		t.Fatalf("ターンは正常完了するはず: inputs=%d", len(svc.inputs))
	}
	if !strings.Contains(out.String(), "[session] 記録に失敗しました") {
		t.Fatalf("記録失敗メッセージが出力に含まれること: %q", out.String())
	}
}

// TestRepl_AgentsMDPathShowsInBanner Options.AgentsMDPath を設定すると
// 起動バナーに読み込んだファイルパスが表示される
func TestRepl_AgentsMDPathShowsInBanner(t *testing.T) {
	svc := fakeSvc{}
	in := strings.NewReader("/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true, AgentsMDPath: "/repo/AGENTS.md"})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if !strings.Contains(out.String(), "AGENTS.md: /repo/AGENTS.md を読み込みました") {
		t.Fatalf("banner missing AGENTS.md path: %q", out.String())
	}
}

// TestRepl_AgentsMDPathEmptyOmitsBannerLine AgentsMDPath 未設定 (既定) では
// バナーにその行が出力されない
func TestRepl_AgentsMDPathEmptyOmitsBannerLine(t *testing.T) {
	svc := fakeSvc{}
	in := strings.NewReader("/quit\n")
	var out bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{Model: "test/m", In: in, Out: &out, DisableSpinner: true})
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	if strings.Contains(out.String(), "AGENTS.md:") {
		t.Fatalf("banner should not mention AGENTS.md: %q", out.String())
	}
}

// TestRepl_ExitsWhenContextCanceledAtPrompt SIGINT で root context がキャンセルされたら、
// プロンプト待ちでブロックしていても REPL は即座にきれいに終了する。
// 従来はループが回り続け、以後の全ターンが即失敗するゾンビセッションになっていた。
func TestRepl_ExitsWhenContextCanceledAtPrompt(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	var out bytes.Buffer
	r := cliui.NewREPL(fakeSvc{}, cliui.Options{Model: "test/m", In: pr, Out: &out, DisableSpinner: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(50 * time.Millisecond) // プロンプト待ちに入るのを待つ
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v, want nil (graceful exit)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("REPL did not exit after context cancellation")
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
