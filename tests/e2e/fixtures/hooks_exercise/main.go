// Package main 09-hooks.md 5.2 節の pre/post ツール実行フックを検証するフィクスチャ。
// 検証対象ツールは fixture 内で定義した touch_probe に統一し、実行の有無を
// 副作用ファイルの存在だけで判定する。stub LLM は D-17 に従い fixture 内で立てる
package main

import (
	"context"
	"encoding/json"
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
)

const (
	stubModel  = "openai/stub"
	toolName   = "touch_probe"
	probeBody  = "touch_probe executed"
	blockedMsg = "blocked by pre_tool_use hook"
)

// touchProbe 副作用ファイルを 1 つ作るだけのフェイクツール。
// allow_binaries の構成に依存せず実行の有無を判定するために shell の代わりに使う
type touchProbe struct{ path string }

func (t touchProbe) Spec() tool.Spec {
	return tool.Spec{
		Name:        toolName,
		Description: "テスト用に副作用ファイルを 1 つ作る",
		Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func (t touchProbe) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	if err := os.WriteFile(t.path, []byte("probe"), 0o600); err != nil {
		return tool.Result{Content: err.Error(), IsError: true}, nil
	}
	return tool.Result{Content: probeBody}, nil
}

// stub 1 回目の stream で touch_probe の ToolCall を返し、以降は平文を返す stub LLM
type stub struct {
	srv *httptest.Server

	mu    sync.Mutex
	calls int
}

func newStub() *stub {
	s := &stub{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *stub) Close() { s.srv.Close() }

func (s *stub) handle(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	payload := `{"choices":[{"delta":{"content":"完了"},"finish_reason":"stop"},{}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
	if first {
		payload = `{"choices":[{"delta":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"` +
			toolName + `","arguments":{}}}]},"finish_reason":"tool_calls"}]}`
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// scenario 1 シナリオの入力
type scenario struct {
	// dir 副作用ファイルを置く一時ディレクトリ
	dir string
	// pre / post 注入する HookSpec
	pre  []agent.HookSpec
	post []agent.HookSpec
	// cancelAfter >0 なら Run 開始からこの時間後に親 ctx をキャンセルする
	cancelAfter time.Duration
}

// outcome 1 シナリオの観測結果
type outcome struct {
	// probed 副作用ファイルが作られたか
	probed bool
	// toolResults EventToolResult の Content 一覧
	toolResults []string
}

// runScenario 1 ターン実行し、副作用とツール結果を観測する
func runScenario(parent context.Context, sc scenario) (outcome, error) {
	probePath := filepath.Join(sc.dir, "probe.txt")
	s := newStub()
	defer s.Close()

	client := openai.New(openai.Options{BaseURL: s.srv.URL, APIKey: "stub"})
	reg := llm.NewRegistry(map[string]llm.Provider{"openai": client})
	tools := tool.NewRegistry([]tool.Tool{touchProbe{path: probePath}}, []string{toolName})
	svc := agent.New(reg, tools, agent.WithHooks(agent.NewHookRunner(sc.pre, sc.post)))

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if sc.cancelAfter > 0 {
		time.AfterFunc(sc.cancelAfter, cancel)
	}

	events := make(chan agent.Event, 64)
	done := make(chan error, 1)
	go func() {
		done <- svc.Run(ctx, agent.Input{
			Model:       stubModel,
			Messages:    []llm.Message{{Role: llm.RoleUser, Content: "実行して"}},
			MaxToolHops: 2,
		}, events)
		close(events)
	}()

	var out outcome
	for ev := range events {
		if ev.Kind == agent.EventToolResult && ev.ToolResult != nil {
			out.toolResults = append(out.toolResults, ev.ToolResult.Content)
		}
	}
	// Run のエラー (親キャンセル等) はシナリオによっては期待される。呼び出し側が判断する
	runErr := <-done
	if _, err := os.Stat(probePath); err == nil {
		out.probed = true
	}
	if runErr != nil && parent.Err() != nil {
		return out, runErr
	}
	return out, nil
}

// mkdir シナリオごとの一時ディレクトリを作る
func mkdir(base, name string) (string, error) {
	dir := filepath.Join(base, name)
	return dir, os.MkdirAll(dir, 0o755)
}

// checkPreDeny exit 2 の pre hook がツールをブロックすることを確認する
func checkPreDeny(ctx context.Context, base string, out io.Writer) error {
	dir, err := mkdir(base, "deny")
	if err != nil {
		return err
	}
	res, err := runScenario(ctx, scenario{dir: dir, pre: []agent.HookSpec{
		{Matcher: toolName, Command: "echo 'policy violation' >&2; exit 2", Timeout: 5 * time.Second},
	}})
	if err != nil {
		return err
	}
	joined := strings.Join(res.toolResults, "\n")
	if !strings.Contains(joined, blockedMsg) || !strings.Contains(joined, "policy violation") {
		return fmt.Errorf("拒否理由がツール結果に無い: %q", joined)
	}
	if res.probed {
		return fmt.Errorf("拒否されたのに副作用ファイルが作られた")
	}
	fmt.Fprintln(out, "hook_pre_deny_blocks=true")
	return nil
}

// checkPreAllow exit 0 の pre hook がツールを通すことを確認する
func checkPreAllow(ctx context.Context, base string, out io.Writer) error {
	dir, err := mkdir(base, "allow")
	if err != nil {
		return err
	}
	res, err := runScenario(ctx, scenario{dir: dir, pre: []agent.HookSpec{
		{Matcher: "*", Command: "exit 0"},
	}})
	if err != nil {
		return err
	}
	if !res.probed {
		return fmt.Errorf("許可されたのに副作用ファイルが作られていない")
	}
	fmt.Fprintln(out, "hook_pre_allow_passes=true")
	return nil
}

// checkPost post hook がツール実行結果を受け取ることを確認する
func checkPost(ctx context.Context, base string, out io.Writer) error {
	dir, err := mkdir(base, "post")
	if err != nil {
		return err
	}
	logPath := filepath.Join(dir, "post.json")
	if _, err := runScenario(ctx, scenario{dir: dir, post: []agent.HookSpec{
		{Matcher: "*", Command: "cat > " + logPath},
	}}); err != nil {
		return err
	}
	body, err := os.ReadFile(logPath) // #nosec G304 -- fixture が組み立てた一時パス
	if err != nil {
		return fmt.Errorf("post hook の出力が読めない: %w", err)
	}
	if err := verifyPostPayload(body); err != nil {
		return err
	}
	fmt.Fprintln(out, "hook_post_receives_result=true")
	return nil
}

// verifyPostPayload post hook の stdin JSON がツール名と実行結果を含むことを確認する。
// キー名は 09-hooks.md R4 が凍結した "tool" / "result" に従う
// (同書 5.2 節 5 は "tool_name" と書いているが R4 と実装が正であり、そちらへ合わせる)
func verifyPostPayload(body []byte) error {
	var p struct {
		Tool   string `json:"tool"`
		Result *struct {
			IsError bool   `json:"is_error"`
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return fmt.Errorf("post hook の JSON 解析: %w", err)
	}
	if p.Tool != toolName {
		return fmt.Errorf("tool=%q want %q", p.Tool, toolName)
	}
	if p.Result == nil {
		return fmt.Errorf("result が含まれていない: %s", body)
	}
	if p.Result.IsError {
		return fmt.Errorf("result.is_error=true want false")
	}
	if !strings.Contains(p.Result.Content, probeBody) {
		return fmt.Errorf("result.content=%q に %q が無い", p.Result.Content, probeBody)
	}
	return nil
}

// checkPreTimeout timeout した pre hook が fail-open で許可扱いになることを確認する
func checkPreTimeout(ctx context.Context, base string, out io.Writer) error {
	dir, err := mkdir(base, "timeout")
	if err != nil {
		return err
	}
	res, err := runScenario(ctx, scenario{dir: dir, pre: []agent.HookSpec{
		{Matcher: "*", Command: "sleep 3", Timeout: 1 * time.Second},
	}})
	if err != nil {
		return err
	}
	if !res.probed {
		return fmt.Errorf("fail-open のはずが副作用ファイルが作られていない")
	}
	fmt.Fprintln(out, "hook_pre_timeout_allows=true")
	return nil
}

// checkParentCancel 親 ctx のキャンセルが fail-open の対象外であることを確認する
func checkParentCancel(ctx context.Context, base string, out io.Writer) error {
	dir, err := mkdir(base, "cancel")
	if err != nil {
		return err
	}
	res, err := runScenario(ctx, scenario{
		dir:         dir,
		pre:         []agent.HookSpec{{Matcher: "*", Command: "sleep 5", Timeout: 10 * time.Second}},
		cancelAfter: 300 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	if res.probed {
		return fmt.Errorf("親キャンセル後に副作用ファイルが作られた")
	}
	fmt.Fprintln(out, "hook_parent_cancel_blocks=true")
	return nil
}

func run(ctx context.Context, base string, out io.Writer) error {
	checks := []func(context.Context, string, io.Writer) error{
		checkPreDeny, checkPreAllow, checkPost, checkPreTimeout, checkParentCancel,
	}
	for _, c := range checks {
		if err := c(ctx, base, out); err != nil {
			return err
		}
	}
	return nil
}

// mainErr os.Exit が defer を実行しないため、cleanup を伴う本体を分離する
func mainErr() error {
	base, err := os.MkdirTemp("", "hooks-e2e-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(base) }()
	return run(context.Background(), base, os.Stdout)
}

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintln(os.Stderr, "hooks_exercise:", err)
		os.Exit(1)
	}
}
