package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// hookToolService pre/post hook 付きで 1 件のツール呼び出しを行う Service を組み立てる
func hookToolService(t *testing.T, hr *agent.HookRunner, calls *int, isError bool, extra ...agent.Option) agent.Service {
	t.Helper()
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "touch_probe", Arguments: json.RawMessage(`{"k":"v"}`)}}},
		{{DeltaText: "done"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{
		namedTool{name: "touch_probe", content: "tool output", isError: isError, calls: calls},
	}, []string{"touch_probe"})
	opts := append([]agent.Option{agent.WithHooks(hr)}, extra...)
	return agent.New(fakeReg{p: prov}, tools, opts...)
}

func hookInput() agent.Input {
	return agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 3,
	}
}

func TestRunReAct_PreHookDenies_SkipsExecution(t *testing.T) {
	calls := 0
	hr := agent.NewHookRunner([]agent.HookSpec{{Matcher: "touch_probe", Command: "echo 'policy violation' >&2; exit 2"}}, nil)
	events, err := collectRun(t, hookToolService(t, hr, &calls, false), hookInput())
	if err != nil {
		t.Fatalf("拒否はターンを打ち切らない期待 got %v", err)
	}
	if calls != 0 {
		t.Fatalf("pre hook 拒否でツールを実行しない期待 got %d", calls)
	}
	var found bool
	for _, ev := range events {
		if ev.Kind == agent.EventToolResult {
			found = true
			if !strings.Contains(ev.ToolResult.Content, "blocked by pre_tool_use hook") {
				t.Fatalf("ブロック理由を含む期待 got %q", ev.ToolResult.Content)
			}
			if !strings.Contains(ev.ToolResult.Content, "policy violation") {
				t.Fatalf("stderr を理由として含む期待 got %q", ev.ToolResult.Content)
			}
			if !ev.ToolResult.IsError {
				t.Fatal("ブロックは IsError 期待")
			}
		}
	}
	if !found {
		t.Fatal("EventToolResult 期待")
	}
}

func TestRunReAct_PreHookAllows_ExecutesTool(t *testing.T) {
	calls := 0
	hr := agent.NewHookRunner([]agent.HookSpec{{Matcher: "*", Command: "exit 0"}}, nil)
	if _, err := collectRun(t, hookToolService(t, hr, &calls, false), hookInput()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("exit 0 なら実行される期待 got %d", calls)
	}
}

func TestRunReAct_PreHookTimeoutAllows(t *testing.T) {
	calls := 0
	hr := agent.NewHookRunner([]agent.HookSpec{{Matcher: "*", Command: "sleep 5", Timeout: 100 * time.Millisecond}}, nil)
	if _, err := collectRun(t, hookToolService(t, hr, &calls, false), hookInput()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("hook timeout は fail-open 期待 got %d", calls)
	}
}

func TestRunReAct_PostHookReceivesToolResult(t *testing.T) {
	calls := 0
	logPath := filepath.Join(t.TempDir(), "post.json")
	hr := agent.NewHookRunner(nil, []agent.HookSpec{{Matcher: "*", Command: "cat > " + logPath}})
	if _, err := collectRun(t, hookToolService(t, hr, &calls, true), hookInput()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("ツールが実行される期待 got %d", calls)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("post hook が 1 回呼ばれる期待 err=%v", err)
	}
	var payload struct {
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args"`
		Result struct {
			IsError bool   `json:"is_error"`
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("payload=%q err=%v", b, err)
	}
	if payload.Tool != "touch_probe" {
		t.Fatalf("ツール名期待 got %q", payload.Tool)
	}
	if !payload.Result.IsError {
		t.Fatal("エラー結果は is_error=true 期待")
	}
	// untrusted マーカー付与前の生の本文が渡る
	if payload.Result.Content != "tool output" {
		t.Fatalf("生のツール結果期待 got %q", payload.Result.Content)
	}
	if string(payload.Args) != `{"k":"v"}` {
		t.Fatalf("引数がそのまま渡る期待 got %s", payload.Args)
	}
}

func TestRunReAct_PostHookRunsAfterExecution(t *testing.T) {
	calls := 0
	dir := t.TempDir()
	logPath := filepath.Join(dir, "order.log")
	// ツール実行は namedTool が行い、post hook はその後に走る。
	// post hook が書いたファイルが存在することで実行後の呼び出しを確認する
	hr := agent.NewHookRunner(nil, []agent.HookSpec{{Matcher: "touch_probe", Command: "echo post >> " + logPath}})
	if _, err := collectRun(t, hookToolService(t, hr, &calls, false), hookInput()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "post") != 1 {
		t.Fatalf("post hook は 1 回だけ呼ばれる期待 got %q", b)
	}
}

func TestRunReAct_HooksNilFieldUnaffected(t *testing.T) {
	calls := 0
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "touch_probe", Arguments: json.RawMessage(`{}`)}}},
		{{DeltaText: "done"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{namedTool{name: "touch_probe", content: "ok", calls: &calls}}, []string{"touch_probe"})
	svc := agent.New(fakeReg{p: prov}, tools)
	if _, err := collectRun(t, svc, hookInput()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("hooks 未設定でも通常どおり実行される期待 got %d", calls)
	}
}

func TestRunReAct_ApprovalDeniedSkipsPreHook(t *testing.T) {
	calls := 0
	probe := filepath.Join(t.TempDir(), "pre_ran")
	hr := agent.NewHookRunner([]agent.HookSpec{{Matcher: "*", Command: "touch " + probe}}, nil)
	svc := hookToolService(t, hr, &calls, false,
		agent.WithApprovalDecider(agent.AutoDecider{Allow: false, Reason: "no"}, []string{"touch_probe"}, time.Second))
	if _, err := collectRun(t, svc, hookInput()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("承認拒否でツールを実行しない期待 got %d", calls)
	}
	if _, err := os.Stat(probe); err == nil {
		t.Fatal("承認拒否のとき pre hook は実行されない期待 (順序 R6)")
	}
}

func TestRunReAct_PostHookReceivesSuccessResult(t *testing.T) {
	calls := 0
	logPath := filepath.Join(t.TempDir(), "post.json")
	hr := agent.NewHookRunner(nil, []agent.HookSpec{{Matcher: "*", Command: "cat > " + logPath}})
	if _, err := collectRun(t, hookToolService(t, hr, &calls, false), hookInput()); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("post hook が 1 回呼ばれる期待 err=%v", err)
	}
	var payload struct {
		Result struct {
			IsError bool   `json:"is_error"`
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("payload=%q err=%v", b, err)
	}
	if payload.Result.IsError {
		t.Fatalf("成功結果は is_error=false 期待 got %+v", payload.Result)
	}
	if payload.Result.Content != "tool output" {
		t.Fatalf("生のツール結果期待 got %q", payload.Result.Content)
	}
}
