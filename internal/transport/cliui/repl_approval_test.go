package cliui_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// approvalTestSvc prompter へ承認を問い合わせ、結果に応じたイベントを流すフェイク
type approvalTestSvc struct {
	prompter *cliui.ApprovalPrompter
	summary  string
	timeout  time.Duration
	executed int
	denied   []string
}

func (s *approvalTestSvc) Run(ctx context.Context, _ agent.Input, out chan<- agent.Event) error {
	call := llm.ToolCall{ID: "c1", Name: "fs_write"}
	out <- agent.Event{Kind: agent.EventToolCall, ToolCall: &call}
	timeout := s.timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	apCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	allowed, reason, err := s.prompter.Decide(apCtx, agent.ApprovalRequest{
		RunID: "default", CallID: "c1", ToolName: "fs_write", Summary: s.summary,
	})
	if err != nil {
		return err
	}
	if !allowed {
		s.denied = append(s.denied, reason)
		out <- agent.Event{Kind: agent.EventToolResult, ToolResult: &agent.ToolResult{
			CallID: "c1", Name: "fs_write", Content: "tool execution denied by reviewer: " + reason, IsError: true,
		}}
	} else {
		s.executed++
		out <- agent.Event{Kind: agent.EventToolResult, ToolResult: &agent.ToolResult{CallID: "c1", Name: "fs_write", Content: "ok"}}
	}
	final := llm.Message{Role: llm.RoleAssistant, Content: "done"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final, TurnMessages: []llm.Message{final}}
	return nil
}

// markerWriter 出力に marker が現れた時点で通知チャネルを close する io.Writer
type markerWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	marker string
	ch     chan struct{}
	fired  bool
}

func newMarkerWriter(marker string) *markerWriter {
	return &markerWriter{marker: marker, ch: make(chan struct{})}
}

func (w *markerWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if !w.fired && strings.Contains(w.buf.String(), w.marker) {
		w.fired = true
		close(w.ch)
	}
	return len(p), nil
}

func (w *markerWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// runApprovalREPL 承認プロンプトが出るまで待ってから answer を書き込み、Run の結果を返す。
// 承認プロンプト表示前に入力を流し込むとバイトが生成中キーとして消費されるため、
// プロンプト表示を待ってから書く
func runApprovalREPL(t *testing.T, svc *approvalTestSvc, prompter *cliui.ApprovalPrompter, answer string) (*markerWriter, error) {
	t.Helper()
	pr, pw := io.Pipe()
	out := newMarkerWriter("approve? [y/N]")
	r := cliui.NewREPL(svc, cliui.Options{
		Model:            "fake/m",
		In:               pr,
		Out:              out,
		DisableSpinner:   true,
		ApprovalPrompter: prompter,
	})
	go func() {
		_, _ = pw.Write([]byte("書き込んで\n"))
		select {
		case <-out.ch:
		case <-time.After(3 * time.Second):
		}
		if answer != "" {
			_, _ = pw.Write([]byte(answer))
		}
		_ = pw.Close()
	}()
	err := r.Run(context.Background())
	_ = pr.Close()
	return out, err
}

func TestRunTurn_ApprovalPrompt_Approve(t *testing.T) {
	prompter := cliui.NewApprovalPrompter()
	svc := &approvalTestSvc{prompter: prompter, summary: "--- a/x\n+++ b/x\n+new line\n"}
	out, err := runApprovalREPL(t, svc, prompter, "y\n")
	if err != nil {
		t.Fatal(err)
	}
	if svc.executed != 1 {
		t.Fatalf("承認でツールが実行される期待 got %d", svc.executed)
	}
	if !strings.Contains(out.String(), "[approval] approved") {
		t.Fatalf("承認表示期待 got %q", out.String())
	}
	if !strings.Contains(out.String(), "[approval] tool=fs_write") {
		t.Fatalf("ツール名表示期待 got %q", out.String())
	}
}

func TestRunTurn_ApprovalPrompt_DenyExplicit(t *testing.T) {
	prompter := cliui.NewApprovalPrompter()
	svc := &approvalTestSvc{prompter: prompter}
	out, err := runApprovalREPL(t, svc, prompter, "n\n")
	if err != nil {
		t.Fatal(err)
	}
	if svc.executed != 0 {
		t.Fatalf("拒否でツールを実行しない期待 got %d", svc.executed)
	}
	if !strings.Contains(out.String(), "[approval] denied") {
		t.Fatalf("拒否表示期待 got %q", out.String())
	}
	if len(svc.denied) != 1 || svc.denied[0] != "denied by user" {
		t.Fatalf("明示的拒否の理由期待 got %v", svc.denied)
	}
}

func TestRunTurn_ApprovalPrompt_DenyDefault(t *testing.T) {
	prompter := cliui.NewApprovalPrompter()
	svc := &approvalTestSvc{prompter: prompter}
	out, err := runApprovalREPL(t, svc, prompter, "\n")
	if err != nil {
		t.Fatal(err)
	}
	if svc.executed != 0 {
		t.Fatalf("空行は既定の拒否期待 got %d", svc.executed)
	}
	if !strings.Contains(out.String(), "[approval] denied") {
		t.Fatalf("拒否表示期待 got %q", out.String())
	}
}

func TestRunTurn_ApprovalPrompt_ShowsSummary(t *testing.T) {
	prompter := cliui.NewApprovalPrompter()
	svc := &approvalTestSvc{prompter: prompter, summary: "--- a/x\n+++ b/x\n-old\n+new\n"}
	out, err := runApprovalREPL(t, svc, prompter, "n\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-old", "+new"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("%q を含む期待 got %q", want, out.String())
		}
	}
}

func TestRunTurn_ApprovalPrompt_ESCInterruptsTurn(t *testing.T) {
	prompter := cliui.NewApprovalPrompter()
	svc := &approvalTestSvc{prompter: prompter}
	out, err := runApprovalREPL(t, svc, prompter, "\x1b")
	if err != nil {
		t.Fatal(err)
	}
	if svc.executed != 0 {
		t.Fatalf("ESC でツールを実行しない期待 got %d", svc.executed)
	}
	if !strings.Contains(out.String(), "[approval] denied (ターンを中断しました)") {
		t.Fatalf("中断表示期待 got %q", out.String())
	}
}

func TestRunTurn_ApprovalPrompt_CtrlCQuitsSession(t *testing.T) {
	prompter := cliui.NewApprovalPrompter()
	svc := &approvalTestSvc{prompter: prompter}
	out, err := runApprovalREPL(t, svc, prompter, "\x03")
	if err != nil {
		t.Fatal(err)
	}
	if svc.executed != 0 {
		t.Fatalf("Ctrl-C でツールを実行しない期待 got %d", svc.executed)
	}
	if !strings.Contains(out.String(), "[approval] denied (セッションを終了します)") {
		t.Fatalf("終了表示期待 got %q", out.String())
	}
}

func TestRunTurn_ApprovalPrompt_TimeoutDenies(t *testing.T) {
	prompter := cliui.NewApprovalPrompter()
	svc := &approvalTestSvc{prompter: prompter, timeout: 60 * time.Millisecond}
	out, err := runApprovalREPL(t, svc, prompter, "")
	if err != nil {
		t.Fatal(err)
	}
	if svc.executed != 0 {
		t.Fatalf("タイムアウトでツールを実行しない期待 got %d", svc.executed)
	}
	if len(svc.denied) != 1 || !strings.Contains(svc.denied[0], "default_decision=deny") {
		t.Fatalf("タイムアウトは default_decision へ解決する期待 got %v", svc.denied)
	}
	_ = out
}

func TestRunTurn_ApprovalPrompt_NilPrompterUnaffected(t *testing.T) {
	var buf bytes.Buffer
	r := cliui.NewREPL(fakeSvc{}, cliui.Options{
		Model:          "fake/m",
		In:             strings.NewReader("hi\n"),
		Out:            &buf,
		DisableSpinner: true,
	})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("既存の通常ターンが変わらず動く期待 got %q", buf.String())
	}
	if strings.Contains(buf.String(), "[approval]") {
		t.Fatalf("承認表示は出ない期待 got %q", buf.String())
	}
}
