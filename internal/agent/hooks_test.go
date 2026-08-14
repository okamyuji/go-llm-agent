package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func preRunner(specs ...HookSpec) *HookRunner  { return NewHookRunner(specs, nil) }
func postRunner(specs ...HookSpec) *HookRunner { return NewHookRunner(nil, specs) }

func TestHookRunner_RunPre_Exit0_Allows(t *testing.T) {
	t.Parallel()
	allowed, reason := preRunner(HookSpec{Matcher: "*", Command: "exit 0"}).RunPre(context.Background(), "shell", json.RawMessage(`{}`))
	if !allowed || reason != "" {
		t.Fatalf("(true,\"\") 期待 got (%v,%q)", allowed, reason)
	}
}

func TestHookRunner_RunPre_Exit2_DeniesWithStderrReason(t *testing.T) {
	t.Parallel()
	allowed, reason := preRunner(HookSpec{Matcher: "*", Command: "echo denied >&2; exit 2"}).RunPre(context.Background(), "shell", json.RawMessage(`{}`))
	if allowed {
		t.Fatal("exit 2 は拒否期待")
	}
	if reason != "denied\n" {
		t.Fatalf("stderr を理由にする期待 got %q", reason)
	}
}

func TestHookRunner_RunPre_OtherExit_WarnsAndAllows(t *testing.T) {
	t.Parallel()
	allowed, _ := preRunner(HookSpec{Matcher: "*", Command: "exit 1"}).RunPre(context.Background(), "shell", json.RawMessage(`{}`))
	if !allowed {
		t.Fatal("exit 2 以外は許可へ倒す期待")
	}
}

func TestHookRunner_RunPre_HookTimeoutStillAllows(t *testing.T) {
	t.Parallel()
	start := time.Now()
	allowed, _ := preRunner(HookSpec{Matcher: "*", Command: "sleep 5", Timeout: 100 * time.Millisecond}).
		RunPre(context.Background(), "shell", json.RawMessage(`{}`))
	if !allowed {
		t.Fatal("hook 自身の timeout は許可へ倒す期待")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout 程度で戻る期待 got %s", elapsed)
	}
}

func TestHookRunner_RunPre_ParentCancelDoesNotAllow(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	allowed, reason := preRunner(HookSpec{Matcher: "*", Command: "sleep 5", Timeout: 10 * time.Second}).
		RunPre(ctx, "shell", json.RawMessage(`{}`))
	if allowed {
		t.Fatal("親 ctx キャンセルは許可へ倒さない期待")
	}
	if reason != "interrupted" {
		t.Fatalf("interrupted 期待 got %q", reason)
	}
}

func TestHookRunner_RunPre_MatcherWildcard(t *testing.T) {
	t.Parallel()
	allowed, _ := preRunner(HookSpec{Matcher: "*", Command: "exit 2"}).RunPre(context.Background(), "anything", json.RawMessage(`{}`))
	if allowed {
		t.Fatal("* は任意ツールに一致する期待")
	}
}

func TestHookRunner_RunPre_MatcherExactNoMatch_Skips(t *testing.T) {
	t.Parallel()
	probe := filepath.Join(t.TempDir(), "probe")
	allowed, _ := preRunner(HookSpec{Matcher: "shell", Command: "touch " + probe + "; exit 2"}).
		RunPre(context.Background(), "fs_write", json.RawMessage(`{}`))
	if !allowed {
		t.Fatal("matcher 不一致の hook は実行されない期待")
	}
	if _, err := os.Stat(probe); err == nil {
		t.Fatal("コマンドが実行されてはならない")
	}
}

func TestHookRunner_RunPre_MultipleHooksStopsAtFirstDeny(t *testing.T) {
	t.Parallel()
	probe := filepath.Join(t.TempDir(), "probe")
	allowed, _ := preRunner(
		HookSpec{Matcher: "*", Command: "exit 2"},
		HookSpec{Matcher: "*", Command: "touch " + probe},
	).RunPre(context.Background(), "shell", json.RawMessage(`{}`))
	if allowed {
		t.Fatal("最初の拒否で打ち切る期待")
	}
	if _, err := os.Stat(probe); err == nil {
		t.Fatal("2 件目の hook は実行されない期待")
	}
}

func TestHookRunner_RunPre_NilRunnerAllowsAlways(t *testing.T) {
	t.Parallel()
	allowed, reason := (*HookRunner)(nil).RunPre(context.Background(), "shell", json.RawMessage(`{}`))
	if !allowed || reason != "" {
		t.Fatalf("nil runner は常に許可期待 got (%v,%q)", allowed, reason)
	}
	(*HookRunner)(nil).RunPost(context.Background(), "shell", json.RawMessage(`{}`), HookResult{})
}

func TestHookRunner_RunPre_StdinContainsToolAndArgsJSON(t *testing.T) {
	t.Parallel()
	out, err := runHook(context.Background(), HookSpec{Matcher: "*", Command: "cat"},
		hookPayload{Tool: "fs_write", Args: json.RawMessage(`{"path":"/tmp/a"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if out.stdout != `{"tool":"fs_write","args":{"path":"/tmp/a"}}` {
		t.Fatalf("pre の payload 期待 got %q", out.stdout)
	}
	if strings.Contains(out.stdout, "result") {
		t.Fatalf("pre では result キーを含めない期待 got %q", out.stdout)
	}
}

func TestHookRunner_RunPost_StdinContainsResult(t *testing.T) {
	t.Parallel()
	logPath := filepath.Join(t.TempDir(), "post.json")
	postRunner(HookSpec{Matcher: "*", Command: "cat > " + logPath}).
		RunPost(context.Background(), "shell", json.RawMessage(`{"command":"ls"}`),
			HookResult{IsError: true, Content: "boom", Duration: 1500 * time.Millisecond})
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Tool   string `json:"tool"`
		Result struct {
			IsError    bool   `json:"is_error"`
			Content    string `json:"content"`
			DurationMS int64  `json:"duration_ms"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("payload=%q err=%v", b, err)
	}
	if payload.Tool != "shell" || !payload.Result.IsError || payload.Result.Content != "boom" || payload.Result.DurationMS != 1500 {
		t.Fatalf("result が渡る期待 got %+v", payload)
	}
}

func TestHookRunner_RunPost_IgnoresExitCode(t *testing.T) {
	t.Parallel()
	// 戻り値が無いため、呼び出しが panic せず戻ることを確認する
	postRunner(HookSpec{Matcher: "*", Command: "exit 2"}).
		RunPost(context.Background(), "shell", json.RawMessage(`{}`), HookResult{})
}

func TestHookRunner_RunPost_MatcherSkips(t *testing.T) {
	t.Parallel()
	probe := filepath.Join(t.TempDir(), "probe")
	postRunner(HookSpec{Matcher: "shell", Command: "touch " + probe}).
		RunPost(context.Background(), "fs_write", json.RawMessage(`{}`), HookResult{})
	if _, err := os.Stat(probe); err == nil {
		t.Fatal("matcher 不一致の post hook は実行されない期待")
	}
}

func TestHookRunner_EnvContainsToolName(t *testing.T) {
	t.Parallel()
	out, err := runHook(context.Background(), HookSpec{Matcher: "*", Command: "echo $GO_LLM_AGENT_TOOL"},
		hookPayload{Tool: "touch_probe", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.stdout, "touch_probe") {
		t.Fatalf("環境変数にツール名が入る期待 got %q", out.stdout)
	}
}

func TestRunHook_DefaultTimeoutApplied(t *testing.T) {
	t.Parallel()
	out, err := runHook(context.Background(), HookSpec{Matcher: "*", Command: "exit 0"}, hookPayload{Tool: "x"})
	if err != nil || out.exitCode != 0 {
		t.Fatalf("timeout 未指定でも実行される期待 got %+v err=%v", out, err)
	}
}

func TestRunHook_TimeoutReportsExitCodeMinusOne(t *testing.T) {
	t.Parallel()
	out, err := runHook(context.Background(), HookSpec{Matcher: "*", Command: "sleep 5", Timeout: 50 * time.Millisecond}, hookPayload{Tool: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if out.exitCode != -1 || !strings.Contains(out.stderr, "timeout after") {
		t.Fatalf("timeout は exitCode=-1 期待 got %+v", out)
	}
	if out.parentCanceled {
		t.Fatal("hook 自身の timeout は親キャンセルではない")
	}
}

func TestHookMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		matcher, tool string
		want          bool
	}{
		{"*", "anything", true},
		{"shell", "shell", true},
		{"shell", "fs_write", false},
		{"", "shell", false},
	}
	for _, tc := range tests {
		if got := hookMatches(tc.matcher, tc.tool); got != tc.want {
			t.Fatalf("matcher=%q tool=%q: want %v got %v", tc.matcher, tc.tool, tc.want, got)
		}
	}
}

// recordingHandler slog レコードの message と tool 属性だけを集める最小ハンドラ
type recordingHandler struct {
	mu      sync.Mutex
	records []recordedLog
}

type recordedLog struct {
	msg  string
	tool string
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	rec := recordedLog{msg: r.Message}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "tool" {
			rec.tool = a.Value.String()
		}
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, rec)
	return nil
}

func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }

// messagesFor 指定ツール名のレコードの message を出現順で返す
func (h *recordingHandler) messagesFor(tool string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.tool == tool {
			out = append(out, r.msg)
		}
	}
	return out
}

// captureLogs 既定 logger を差し替え、テスト終了時に元へ戻す。
// slog.Default はプロセス全体で共有されるため t.Parallel とは併用しない
func captureLogs(t *testing.T) *recordingHandler {
	t.Helper()
	h := &recordingHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

func TestHookRunner_RunPost_LogsOnlyExitCodeWarning(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		command  string
		want     []string
	}{
		{
			name:     "非 0 終了は exited non-zero だけを警告する",
			toolName: "post_log_probe_nonzero",
			command:  "exit 3",
			want:     []string{"post_tool_use hook exited non-zero"},
		},
		{
			name:     "正常終了なら警告を出さない",
			toolName: "post_log_probe_zero",
			command:  "exit 0",
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := captureLogs(t)
			postRunner(HookSpec{Matcher: tc.toolName, Command: tc.command}).
				RunPost(context.Background(), tc.toolName, json.RawMessage(`{}`), HookResult{})
			got := h.messagesFor(tc.toolName)
			if len(got) != len(tc.want) {
				t.Fatalf("want %v got %v", tc.want, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("want %v got %v", tc.want, got)
				}
			}
		})
	}
}

func TestRunHook_ParentCancelReportsExitCodeMinusOne(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := runHook(ctx, HookSpec{Matcher: "*", Command: "sleep 5", Timeout: 10 * time.Second}, hookPayload{Tool: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !out.parentCanceled {
		t.Fatalf("親キャンセル期待 got %+v", out)
	}
	if out.exitCode != -1 {
		t.Fatalf("exitCode=-1 期待 got %d", out.exitCode)
	}
	if out.stderr != "canceled by parent context" {
		t.Fatalf("親キャンセルの stderr 期待 got %q", out.stderr)
	}
}
