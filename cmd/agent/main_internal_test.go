package main

import (
	"context"
	"testing"

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
	opts, approver, err := agentOptions(&config.Config{}, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
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
	opts, approver, err := agentOptions(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
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
	opts, approver, err := agentOptions(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), d)
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
	opts, _, err := agentOptions(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
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
	opts, _, err := agentOptions(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
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
	opts, _, err := agentOptions(cfg, tool.NewRegistry(nil, nil), nil, t.TempDir(), nil)
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
