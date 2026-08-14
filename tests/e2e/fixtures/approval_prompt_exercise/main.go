// Package main 04-approval.md 5.2 節の対話承認 (y / n / timeout / 致命的失敗 /
// 既読レジストリ) を検証するフィクスチャ。パイプ駆動の in-process 構成で、
// 実バイナリも PTY も使わない。stub LLM は D-17 に従い fixture 内で立てる
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/openai"
	"github.com/okamyuji/go-llm-agent/internal/tool"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

const (
	stubModel   = "openai/stub"
	writeBody   = "こんにちは\n"
	deniedMark  = "[approval] denied"
	promptInput = "書き込んで\n"
)

// stub 1 回目の stream で fs_write の ToolCall を返し、以降は平文を返す stub LLM
type stub struct {
	srv *httptest.Server
	// targetPath fs_write の書き込み先
	targetPath string

	mu    sync.Mutex
	calls int
	// bodies 受け取ったリクエストの生 JSON
	bodies []string
}

func newStub(targetPath string) *stub {
	s := &stub{targetPath: targetPath}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *stub) Close() { s.srv.Close() }

func (s *stub) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.bodies = append(s.bodies, string(body))
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	if first {
		args, _ := json.Marshal(map[string]string{"path": s.targetPath, "content": writeBody})
		call, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"tool_calls": []map[string]any{{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "fs_write", "arguments": json.RawMessage(args)},
				}}},
				"finish_reason": "tool_calls",
			}},
		})
		writeChunk(w, string(call))
	} else {
		writeChunk(w, `{"choices":[{"delta":{"content":"完了しました"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func writeChunk(w http.ResponseWriter, payload string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *stub) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// bodiesContain 受け取った全リクエストのいずれかが sub を含むか
func (s *stub) bodiesContain(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.bodies {
		if strings.Contains(b, sub) {
			return true
		}
	}
	return false
}

// harness 1 シナリオ分の REPL 構成
type harness struct {
	stub    *stub
	tools   tool.Registry
	edit    *tool.FSEdit
	svc     agent.Service
	prompt  *cliui.ApprovalPrompter
	target  string
	baseDir string
}

// newHarness 一時ディレクトリを allow_paths とするツール群と stub 向け Service を組む。
// decider が nil なら対話承認 (ApprovalPrompter) を使う。
// baseDir は Sandbox 生成前に作る (存在しないパスは EvalSymlinks に失敗し、
// macOS の /var -> /private/var のような symlink 解決差で許可判定が食い違うため)
func newHarness(baseDir string, timeout time.Duration, decider agent.ApprovalDecider) (*harness, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	target := filepath.Join(baseDir, "out.txt")
	s := newStub(target)
	sb := tool.NewSandbox([]string{baseDir})
	rr := tool.NewReadRegistry()
	edit := tool.NewFSEdit(sb, rr, nil)
	tools := tool.NewRegistry(
		[]tool.Tool{
			tool.NewFSReadWithLogger(sb, 1<<20, nil, rr),
			tool.NewFSWriteWithLogger(sb, nil, rr),
			edit,
		},
		[]string{"fs_read", "fs_write", "fs_edit"},
	)
	prompter := cliui.NewApprovalPrompter()
	var d agent.ApprovalDecider = prompter
	if decider != nil {
		d = decider
	}
	client := openai.New(openai.Options{BaseURL: s.srv.URL, APIKey: "stub"})
	reg := llm.NewRegistry(map[string]llm.Provider{"openai": client})
	svc := agent.New(reg, tools, agent.WithApprovalDecider(d, []string{"fs_write"}, timeout))
	return &harness{stub: s, tools: tools, edit: edit, svc: svc, prompt: prompter, target: target, baseDir: baseDir}, nil
}

func (h *harness) Close() { h.stub.Close() }

// run REPL を 1 回起動する。in / out は呼び出し側が与える
func (h *harness) run(ctx context.Context, in io.Reader, out io.Writer) error {
	return cliui.NewREPL(h.svc, cliui.Options{
		Model:            stubModel,
		In:               in,
		Out:              out,
		DisableSpinner:   true,
		ApprovalPrompter: h.prompt,
	}).Run(ctx)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// checkApprove y で承認され、diff preview が出て、ファイルが作成されることを確認する
func checkApprove(ctx context.Context, baseDir string, out io.Writer) error {
	h, err := newHarness(filepath.Join(baseDir, "approve"), 2*time.Second, nil)
	if err != nil {
		return err
	}
	defer h.Close()
	var buf bytes.Buffer
	if err := h.run(ctx, strings.NewReader(promptInput+"y\n"), &buf); err != nil {
		return fmt.Errorf("approve シナリオの Run: %w", err)
	}
	got := buf.String()
	if !strings.Contains(got, "[approval] tool=fs_write") {
		return fmt.Errorf("承認プロンプトが出ていない: %q", got)
	}
	if !strings.Contains(got, "[approval] approved") {
		return fmt.Errorf("承認されていない: %q", got)
	}
	if !fileExists(h.target) {
		return fmt.Errorf("承認したのにファイルが作成されていない: %s", h.target)
	}
	fmt.Fprintln(out, "approval_yes_writes_file=true")
	if !strings.Contains(got, "+"+strings.TrimSuffix(writeBody, "\n")) {
		return fmt.Errorf("diff の + 行が出ていない: %q", got)
	}
	fmt.Fprintln(out, "approval_shows_diff=true")
	return nil
}

// checkDeny n で拒否され、ファイルが作成されないことを確認する。
// 併せて拒否後も既読レジストリが汚染されていないことを FSEdit で確認する
func checkDeny(ctx context.Context, baseDir string, out io.Writer) error {
	h, err := newHarness(filepath.Join(baseDir, "deny"), 2*time.Second, nil)
	if err != nil {
		return err
	}
	defer h.Close()
	var buf bytes.Buffer
	if err := h.run(ctx, strings.NewReader(promptInput+"n\n"), &buf); err != nil {
		return fmt.Errorf("deny シナリオの Run: %w", err)
	}
	got := buf.String()
	if !strings.Contains(got, deniedMark) {
		return fmt.Errorf("拒否されていない: %q", got)
	}
	if fileExists(h.target) {
		return fmt.Errorf("拒否したのにファイルが作成された: %s", h.target)
	}
	fmt.Fprintln(out, "approval_no_skips_write=true")
	return checkRegistryClean(ctx, h, out)
}

// checkRegistryClean 承認拒否されたパスが「既読」として登録されていないことを確認する
func checkRegistryClean(ctx context.Context, h *harness, out io.Writer) error {
	args, err := json.Marshal(map[string]string{"path": h.target, "old_string": "a", "new_string": "b"})
	if err != nil {
		return err
	}
	res, err := h.edit.Execute(ctx, args)
	if err != nil {
		return fmt.Errorf("fs_edit の Execute: %w", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "was not read in this session") {
		return fmt.Errorf("未読チェックが働いていない: %+v", res)
	}
	fmt.Fprintln(out, "approval_summary_keeps_registry_clean=true")
	return nil
}

// markerWriter 累積出力に marker が初めて現れた時点で notify を close する Writer。
// Write は REPL の単一 goroutine からのみ呼ばれるため排他は不要
type markerWriter struct {
	buf    bytes.Buffer
	marker string
	notify chan struct{}
	fired  bool
}

func newMarkerWriter(marker string) *markerWriter {
	return &markerWriter{marker: marker, notify: make(chan struct{})}
}

func (m *markerWriter) Write(p []byte) (int, error) {
	n, err := m.buf.Write(p)
	if !m.fired && strings.Contains(m.buf.String(), m.marker) {
		m.fired = true
		close(m.notify)
	}
	return n, err
}

// checkTimeout 応答を書かずに待つと timeout で deny されることを確認する。
// pipe への書き込みと Close はすべて writer goroutine が行い、Run は main goroutine が呼ぶ
func checkTimeout(ctx context.Context, baseDir string, out io.Writer) error {
	h, err := newHarness(filepath.Join(baseDir, "timeout"), 1*time.Second, nil)
	if err != nil {
		return err
	}
	defer h.Close()
	pr, pw := io.Pipe()
	mw := newMarkerWriter(deniedMark)

	elapsed := make(chan time.Duration, 1)
	go func() {
		if _, err := io.WriteString(pw, promptInput); err != nil {
			_ = pw.CloseWithError(err)
			elapsed <- 0
			return
		}
		t0 := time.Now()
		<-mw.notify
		elapsed <- time.Since(t0)
		_ = pw.Close()
	}()

	if err := h.run(ctx, pr, mw); err != nil {
		return fmt.Errorf("timeout シナリオの Run: %w", err)
	}
	d := <-elapsed
	if d < time.Second {
		return fmt.Errorf("deny までの経過時間が %v で 1s 未満", d)
	}
	if !strings.Contains(mw.buf.String(), deniedMark) {
		return fmt.Errorf("timeout で拒否されていない: %q", mw.buf.String())
	}
	if fileExists(h.target) {
		return fmt.Errorf("timeout 拒否なのにファイルが作成された: %s", h.target)
	}
	fmt.Fprintln(out, "approval_timeout_denies=true")
	return nil
}

// failingDecider 承認機構自体の致命的失敗を模す
type failingDecider struct{}

func (failingDecider) Decide(_ context.Context, _ agent.ApprovalRequest) (bool, string, error) {
	return false, "", errors.New("approval broker unavailable")
}

// checkFatal 致命的失敗でターンが打ち切られ、拒否結果が履歴へ積まれないことを確認する
func checkFatal(ctx context.Context, baseDir string, out io.Writer) error {
	h, err := newHarness(filepath.Join(baseDir, "fatal"), 2*time.Second, failingDecider{})
	if err != nil {
		return err
	}
	defer h.Close()
	var buf bytes.Buffer
	// 応答行を与えないのは、致命的失敗でターンが打ち切られたあとの残り行が
	// 次のプロンプトとして読まれ、LLM 呼び出し回数の表明が崩れるため
	if err := h.run(ctx, strings.NewReader(promptInput), &buf); err != nil {
		return fmt.Errorf("fatal シナリオの Run: %w", err)
	}
	if fileExists(h.target) {
		return fmt.Errorf("致命的失敗なのにファイルが作成された: %s", h.target)
	}
	if n := h.stub.callCount(); n != 1 {
		return fmt.Errorf("LLM 呼び出しが %d 回。ターンが打ち切られていない", n)
	}
	if h.stub.bodiesContain("tool execution denied by reviewer") {
		return fmt.Errorf("拒否のツール結果が履歴へ積まれた")
	}
	fmt.Fprintln(out, "approval_fatal_error_aborts_turn=true")
	return nil
}

func run(ctx context.Context, baseDir string, out io.Writer) error {
	checks := []func(context.Context, string, io.Writer) error{
		checkApprove, checkDeny, checkTimeout, checkFatal,
	}
	for _, c := range checks {
		if err := c(ctx, baseDir, out); err != nil {
			return err
		}
	}
	return nil
}

// mainErr os.Exit が defer を実行しないため、cleanup を伴う本体を分離する
func mainErr() error {
	base, err := os.MkdirTemp("", "approval-e2e-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(base) }()
	return run(context.Background(), base, os.Stdout)
}

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "approval_prompt_exercise:", err)
		os.Exit(1)
	}
}
