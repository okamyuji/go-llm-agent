package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// stubDecider agentOptions へ注入する ApprovalDecider のテストダブル
type stubDecider struct{ calls int }

func (d *stubDecider) Decide(_ context.Context, _ agent.ApprovalRequest) (bool, string, error) {
	d.calls++
	return true, "", nil
}

func approvalConfig(tools ...string) *config.Config {
	cfg := &config.Config{}
	cfg.Agent.Approval.RequiredTools = tools
	cfg.Agent.Approval.TimeoutSeconds = 30
	return cfg
}

func TestApprovalOption_NilDeciderCreatesBroker(t *testing.T) {
	opt, approver := approvalOption(approvalConfig("shell"), nil)
	if opt == nil {
		t.Fatal("Option が返る期待")
	}
	if approver == nil {
		t.Fatal("decider 未指定なら HTTPApprover を生成する期待")
	}
}

func TestApprovalOption_InjectedDeciderSkipsBroker(t *testing.T) {
	opt, approver := approvalOption(approvalConfig("shell"), &stubDecider{})
	if opt == nil {
		t.Fatal("Option が返る期待")
	}
	if approver != nil {
		t.Fatal("decider 指定時は broker を生成しない期待")
	}
}

// baselineOptionCount 何も設定しない config が返すオプション数。
// 個別キーの効果は差分で判定する
func baselineOptionCount(t *testing.T) int {
	t.Helper()
	opts, approver, err := agentOptionsWithDecider(&config.Config{}, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if approver != nil {
		t.Fatal("required_tools が空なら承認機構を組み立てない期待")
	}
	return len(opts)
}

func TestAgentOptions_NoApprovalRequiredToolsSkipsApprover(t *testing.T) {
	if n := baselineOptionCount(t); n < 0 {
		t.Fatalf("オプション数が負 got %d", n)
	}
}

func TestAgentOptions_ApprovalRequiredBuildsBroker(t *testing.T) {
	cfg := approvalConfig("shell")
	opts, approver, err := agentOptionsWithDecider(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if approver == nil {
		t.Fatal("serve 経路では broker を返す期待")
	}
	if len(opts) != baselineOptionCount(t)+1 {
		t.Fatalf("承認オプションが 1 件増える期待 got %d", len(opts))
	}
}

func TestAgentOptions_ApprovalWithDeciderUsesInjected(t *testing.T) {
	cfg := approvalConfig("shell")
	d := &stubDecider{}
	opts, approver, err := agentOptionsWithDecider(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), d)
	if err != nil {
		t.Fatal(err)
	}
	if approver != nil {
		t.Fatal("対話プロンプタ注入時は broker を生成しない期待")
	}
	if len(opts) != baselineOptionCount(t)+1 {
		t.Fatalf("承認オプションが 1 件増える期待 got %d", len(opts))
	}
}

func TestAgentOptions_ToolResultLimitOptionAdded(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.ToolResultLimit.MaxChars = 100
	opts, _, err := agentOptionsWithDecider(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != baselineOptionCount(t)+1 {
		t.Fatalf("切り詰めオプションが 1 件増える期待 got %d", len(opts))
	}
}

func TestAgentOptions_ToolValidationBuildsValidator(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.ToolValidation.Enabled = true
	cfg.Agent.ToolValidation.MaxRetries = 2
	opts, _, err := agentOptionsWithDecider(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// validator と retries の 2 件
	if len(opts) != baselineOptionCount(t)+2 {
		t.Fatalf("検証オプションが 2 件増える期待 got %d", len(opts))
	}
}

func TestAgentOptions_ToolChoiceOptionAdded(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.ToolChoice.Mode = "none"
	opts, _, err := agentOptionsWithDecider(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != baselineOptionCount(t)+1 {
		t.Fatalf("tool_choice オプションが 1 件増える期待 got %d", len(opts))
	}
}

func TestExcludeTool(t *testing.T) {
	got := excludeTool([]string{"a", "fs_edit", "b"}, "fs_edit")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("指定ツールのみ除外する期待 got %v", got)
	}
}

func TestChatApprovalWiring_NoRequiredTools(t *testing.T) {
	p, d := chatApprovalWiring(&config.Config{})
	if p != nil || d != nil {
		t.Fatalf("required_tools が空なら対話プロンプタを作らない期待 got %v %v", p, d)
	}
}

func TestChatApprovalWiring_CreatesPrompter(t *testing.T) {
	p, d := chatApprovalWiring(approvalConfig("fs_write"))
	if p == nil || d == nil {
		t.Fatal("対話プロンプタを作る期待")
	}
	if d != agent.ApprovalDecider(p) {
		t.Fatal("同じ prompter を decider として渡す期待")
	}
}

func TestChatHistoryFile(t *testing.T) {
	got := chatHistoryFile()
	home, err := os.UserHomeDir()
	if err != nil {
		if got != "" {
			t.Fatalf("ホーム解決不可なら空文字期待 got %q", got)
		}
		return
	}
	if got != filepath.Join(home, ".agent_history") {
		t.Fatalf("~/.agent_history 期待 got %q", got)
	}
}

// writeChatConfig runChatSession が読める最小構成の config.yaml を書く
func writeChatConfig(t *testing.T, dir, extra string) string {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "dummy")
	path := filepath.Join(dir, "config.yaml")
	cfg := "default_model: openai/gpt-4.1-mini\n" +
		"providers:\n" +
		"  openai:\n" +
		"    base_url: http://127.0.0.1:1\n" +
		"    api_key_env: OPENAI_API_KEY\n" +
		"agent:\n" +
		"  max_tool_hops: 2\n" +
		"  enabled_tools: []\n" + extra +
		"tools:\n" +
		"  fs:\n" +
		"    allow_paths: [\"" + dir + "\"]\n" +
		"  shell:\n" +
		"    timeout_seconds: 5\n" +
		"    max_timeout_seconds: 10\n" +
		"    allow_binaries: []\n" +
		"  http_fetch: {}\n" +
		"  search_files: {}\n" +
		"server:\n" +
		"  addr: 127.0.0.1:0\n" +
		"storage:\n" +
		"  sessions_dir: " + dir + "\n" +
		"logging:\n" +
		"  format: text\n" +
		"  level: info\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunChatSession_QuitsImmediately(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "")
	var out bytes.Buffer
	err := runChatSession(context.Background(), chatSessionParams{
		ConfigPath: path,
		NoSpinner:  true,
		In:         strings.NewReader("/quit\n"),
		Out:        &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "REPL") {
		t.Fatalf("REPL バナー期待 got %q", out.String())
	}
}

func TestRunChatSession_WithApprovalRequiredTools(t *testing.T) {
	dir := t.TempDir()
	path := writeChatConfig(t, dir, "  approval:\n    required_tools: [\"fs_write\"]\n    timeout_seconds: 5\n    default_decision: deny\n")
	var out bytes.Buffer
	err := runChatSession(context.Background(), chatSessionParams{
		ConfigPath: path,
		Model:      "openai/gpt-4.1-mini",
		NoSpinner:  true,
		In:         strings.NewReader("/quit\n"),
		Out:        &out,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunChatSession_ConfigLoadError(t *testing.T) {
	err := runChatSession(context.Background(), chatSessionParams{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		In:         strings.NewReader("/quit\n"),
		Out:        io.Discard,
	})
	if err == nil {
		t.Fatal("設定読み込み失敗はエラー期待")
	}
}

func TestAgentOptions_StrategyAndEnricherAndBilling(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Strategy = "planner_executor"
	cfg.Agent.PlannerExecutor.MaxSteps = 2
	cfg.Agent.Enricher.Enabled = true
	cfg.Agent.Enricher.Languages = map[string]string{"go": "go.md"}
	base := baselineOptionCount(t)
	opts, _, err := agentOptionsWithDecider(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) <= base {
		t.Fatalf("strategy / enricher でオプションが増える期待 got %d (base %d)", len(opts), base)
	}
}

func TestAgentOptions_InvalidScannerPatternErrors(t *testing.T) {
	cfg := &config.Config{}
	cfg.Safety.InputScanner.Enabled = true
	cfg.Safety.InputScanner.Patterns = []config.SafetyInputScannerRule{{ID: "bad", Regex: "("}}
	if _, _, err := agentOptionsWithDecider(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil); err == nil {
		t.Fatal("不正な正規表現はエラー期待")
	}
}

func TestAgentOptions_InvalidRedactorPatternErrors(t *testing.T) {
	cfg := &config.Config{}
	cfg.Safety.OutputRedactor.Enabled = true
	cfg.Safety.OutputRedactor.Rules = []config.SafetyOutputRedactorRule{{ID: "bad", Regex: "(", Replacement: "x"}}
	if _, _, err := agentOptionsWithDecider(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil); err == nil {
		t.Fatal("不正な正規表現はエラー期待")
	}
}

func TestHookRunner_NilWhenNoHooks(t *testing.T) {
	if hr := hookRunner(config.HooksConfig{}); hr != nil {
		t.Fatal("hooks 未設定なら nil 期待")
	}
}

func TestHookRunner_BuiltFromConfig(t *testing.T) {
	h := config.HooksConfig{
		PreToolUse:  []config.HookConfig{{Matcher: "shell", Command: "exit 0", TimeoutSeconds: 3}},
		PostToolUse: []config.HookConfig{{Matcher: "*", Command: "audit"}},
	}
	if hr := hookRunner(h); hr == nil {
		t.Fatal("hooks 設定時は HookRunner 期待")
	}
}

func TestToHookSpecs_ConvertsTimeout(t *testing.T) {
	got := toHookSpecs([]config.HookConfig{{Matcher: "*", Command: "c", TimeoutSeconds: 7}})
	if len(got) != 1 || got[0].Timeout != 7*time.Second || got[0].Matcher != "*" || got[0].Command != "c" {
		t.Fatalf("変換結果期待 got %+v", got)
	}
	if len(toHookSpecs(nil)) != 0 {
		t.Fatal("空入力は空出力期待")
	}
}

func TestAgentOptions_HooksOptionAdded(t *testing.T) {
	cfg := &config.Config{}
	cfg.Hooks.PreToolUse = []config.HookConfig{{Matcher: "*", Command: "exit 0"}}
	opts, _, err := agentOptionsWithDecider(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != baselineOptionCount(t)+1 {
		t.Fatalf("hooks オプションが 1 件増える期待 got %d", len(opts))
	}
}
