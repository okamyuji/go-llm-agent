package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// errStream Recv で 1 件エラーイベントを返し、Close の戻り値を差し替えられる
type errStream struct {
	recvErr  error
	closeErr error
	done     bool
}

func (s *errStream) Recv() (llm.StreamEvent, bool) {
	if s.done {
		return llm.StreamEvent{}, false
	}
	s.done = true
	return llm.StreamEvent{Err: s.recvErr}, true
}
func (s *errStream) Close() error { return s.closeErr }

type errStreamProvider struct{ stream *errStream }

func (p *errStreamProvider) Name() string { return "errstream" }
func (p *errStreamProvider) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, nil
}
func (p *errStreamProvider) Stream(context.Context, llm.ChatRequest) (llm.ChatStream, error) {
	return p.stream, nil
}

// captureSlog 既定 logger を差し替え、出力文字列を返す関数を渡す。
// slog.SetDefault はプロセス全体の状態なので t.Cleanup で必ず復旧する
func captureSlog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf strings.Builder
	prev := slog.Default()
	h := slog.NewTextHandler(&lockedWriter{mu: &mu, b: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// ストリーム受信エラー後の Close 失敗は警告ログにするが戻り値は受信エラーのままにする。
// Close が成功した場合は警告を出さない
func TestRunReAct_StreamRecvError_ClosePathLogging(t *testing.T) {
	recvErr := errors.New("recv boom")
	tests := []struct {
		name     string
		closeErr error
		wantLog  bool
	}{
		{"Close 失敗は警告ログを出す", errors.New("close boom"), true},
		{"Close 成功なら警告ログを出さない", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureSlog(t)
			prov := &errStreamProvider{stream: &errStream{recvErr: recvErr, closeErr: tt.closeErr}}
			svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))
			_, err := collectRun(t, svc, agent.Input{
				Model:       "fake/m",
				Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
				MaxToolHops: 1,
			})
			if !errors.Is(err, recvErr) {
				t.Fatalf("受信エラーをそのまま返す期待 got %v", err)
			}
			got := strings.Contains(logs(), "llm stream close failed after recv error")
			if got != tt.wantLog {
				t.Fatalf("警告ログ有無 want %v got %v (logs=%q)", tt.wantLog, got, logs())
			}
		})
	}
}

// deadlineDecider ctx の deadline までの残り時間を記録する
type deadlineDecider struct {
	remaining   time.Duration
	hasDeadline bool
}

func (d *deadlineDecider) Decide(ctx context.Context, _ agent.ApprovalRequest) (bool, string, error) {
	dl, ok := ctx.Deadline()
	d.hasDeadline = ok
	if ok {
		d.remaining = time.Until(dl)
	}
	return true, "", nil
}

// approvalTimeout 未指定 (0) は defaultApprovalTimeout (5 分) へ落ちる。
// 0 のまま context.WithTimeout に渡すと deadline が即座に過ぎるため、
// decider に届く残り時間が正の大きな値であることで境界を表明する
func TestRunReAct_ApprovalTimeoutZero_FallsBackToDefault(t *testing.T) {
	calls := 0
	d := &deadlineDecider{}
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "fs_write", Arguments: json.RawMessage(`{}`)}}},
		{{DeltaText: "done"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{namedTool{name: "fs_write", content: "executed", calls: &calls}}, []string{"fs_write"})
	svc := agent.New(fakeReg{p: prov}, tools, agent.WithApprovalDecider(d, []string{"fs_write"}, 0))

	if _, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "書き込んで"}},
		MaxToolHops: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if !d.hasDeadline {
		t.Fatal("承認 ctx には deadline が必要 (無期限待機は goroutine leak を招く)")
	}
	if d.remaining < 4*time.Minute {
		t.Fatalf("timeout=0 は既定 5 分へ落ちる期待 got 残り %v", d.remaining)
	}
}
