package cliui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// pasteFixture tests/pastedata の fixture を読み、CRLF を LF へ正規化して返す
func pasteFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "pastedata", name))
	if err != nil {
		t.Fatalf("fixture %s 読み込み失敗: %v", name, err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// asPasteBytes 端末の bracketed paste が送るバイト列 (改行は CR) を組み立てる
func asPasteBytes(content string) string {
	return "\x1b[200~" + strings.ReplaceAll(content, "\n", "\r") + "\x1b[201~"
}

// syncBuffer 複数 goroutine から書かれる出力バッファ
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// blockingTurnSvc 最初のターンだけ started を通知して release まで待つ。
// 2 ターン目以降は即座に final を返す
type blockingTurnSvc struct {
	mu      sync.Mutex
	inputs  []agent.Input
	started chan struct{}
	release chan struct{}
}

func (s *blockingTurnSvc) Run(ctx context.Context, in agent.Input, out chan<- agent.Event) error {
	s.mu.Lock()
	s.inputs = append(s.inputs, in)
	n := len(s.inputs)
	s.mu.Unlock()
	if n == 1 {
		s.started <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	final := llm.Message{Role: llm.RoleAssistant, Content: "ok"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final, TurnMessages: []llm.Message{final}}
	return nil
}

// instantSvc 常に即座に final を返し、届いた入力を記録する
type instantSvc struct {
	mu     sync.Mutex
	inputs []agent.Input
}

func (s *instantSvc) Run(_ context.Context, in agent.Input, out chan<- agent.Event) error {
	s.mu.Lock()
	s.inputs = append(s.inputs, in)
	s.mu.Unlock()
	final := llm.Message{Role: llm.RoleAssistant, Content: "ok"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final, TurnMessages: []llm.Message{final}}
	return nil
}

// lastUserContent 入力メッセージ列の最後の user メッセージ本文を返す
func lastUserContent(in agent.Input) string {
	for i := len(in.Messages) - 1; i >= 0; i-- {
		if in.Messages[i].Role == llm.RoleUser {
			return in.Messages[i].Content
		}
	}
	return ""
}

// TestEscSequenceGrace_Range 生成中の単独 ESC 判定の待ち時間は、エスケープ列の
// 後続バイトを取りこぼさない下限と、中断の体感遅延を悪化させない上限に収まる
func TestEscSequenceGrace_Range(t *testing.T) {
	if escSequenceGrace < 10*time.Millisecond || escSequenceGrace > 200*time.Millisecond {
		t.Fatalf("escSequenceGrace=%v, want 10ms..200ms", escSequenceGrace)
	}
}

// TestREPL_PasteDuringTurn_NotSplitIntoLineTurns 生成中に届いた複数行ペーストが
// 行ごとの個別ターンとして誤発火せず、次のプロンプトで 1 件の入力にまとまる。
// コピーした ">> " 付き agent 出力の再ペーストという報告症状の再現
func TestREPL_PasteDuringTurn_NotSplitIntoLineTurns(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open err=%v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })

	svc := &blockingTurnSvc{started: make(chan struct{}, 1), release: make(chan struct{}, 1)}
	out := &syncBuffer{}
	r := NewREPL(svc, Options{Model: "test/m", In: tty, Out: out, DisableSpinner: true})

	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()

	if _, err := ptmx.WriteString("hello\r"); err != nil {
		t.Fatalf("write err=%v", err)
	}
	select {
	case <-svc.started:
	case <-time.After(5 * time.Second):
		t.Fatal("最初のターンが開始しない")
	}

	want := pasteFixture(t, "agent-output-with-prompt.txt")
	// ターン実行中にペースト全体と、その後の送信 Enter・/quit を流し込む
	if _, err := ptmx.WriteString(asPasteBytes(want) + "\r/quit\r"); err != nil {
		t.Fatalf("write err=%v", err)
	}
	svc.release <- struct{}{}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("REPL が終了しない (ロックアウトの疑い)")
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.inputs) != 2 {
		t.Fatalf("ターン数=%d, want 2 (ペーストが行単位に分割されて誤発火した疑い)", len(svc.inputs))
	}
	got := lastUserContent(svc.inputs[1])
	if got != strings.TrimSuffix(want, "\n") && got != want {
		t.Fatalf("2 ターン目の入力がペースト全文でない:\ngot=%q", got)
	}
}

// TestREPL_PasteWithEscBytes_QuitStillWorks ESC バイトを含む ANSI ログ風の
// ペースト後も /quit が効く。終了マーカーの食い潰しで pasteActive が残留し
// Ctrl-C も /quit も効かなくなるロックアウトの再現
func TestREPL_PasteWithEscBytes_QuitStillWorks(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open err=%v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })

	svc := &instantSvc{}
	out := &syncBuffer{}
	r := NewREPL(svc, Options{Model: "test/m", In: tty, Out: out, DisableSpinner: true})

	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()

	content := pasteFixture(t, "ansi-escape-log.txt")
	if _, err := ptmx.WriteString(asPasteBytes(content) + "\r/quit\r"); err != nil {
		t.Fatalf("write err=%v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("REPL がロックアウト: ペースト後に /quit が効かない")
	}

	svc.mu.Lock()
	defer svc.mu.Unlock()
	if len(svc.inputs) != 1 {
		t.Fatalf("ターン数=%d, want 1", len(svc.inputs))
	}
	if got := lastUserContent(svc.inputs[0]); !strings.Contains(got, "ERROR") {
		t.Fatalf("ペースト内容が届いていない: %q", got)
	}
}
