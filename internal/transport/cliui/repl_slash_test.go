package cliui_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// tokenSvc ターンごとに固定の EventUsage を 1 件返すフェイク
type tokenSvc struct {
	in    int
	out   int
	calls int
}

func (s *tokenSvc) Run(_ context.Context, _ agent.Input, out chan<- agent.Event) error {
	s.calls++
	out <- agent.Event{Kind: agent.EventUsage, Usage: &llm.Usage{InputTokens: s.in, OutputTokens: s.out}}
	final := llm.Message{Role: llm.RoleAssistant, Content: "answer"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final, TurnMessages: []llm.Message{final}}
	return nil
}

// usageOnlyErrorSvc usage だけ届いて生成結果が無いまま失敗するターン。
// 中断・エラーでも課金済みトークンが累計へ積まれることの検証に使う
type usageOnlyErrorSvc struct{}

func (usageOnlyErrorSvc) Run(_ context.Context, _ agent.Input, out chan<- agent.Event) error {
	out <- agent.Event{Kind: agent.EventUsage, Usage: &llm.Usage{InputTokens: 7, OutputTokens: 3}}
	out <- agent.Event{Kind: agent.EventError, Err: context.DeadlineExceeded}
	return nil
}

// fakeBilling セッション ID ごとの Snapshot を返す billing.Accumulator のフェイク
type fakeBilling struct {
	snaps map[string]billing.Snapshot
}

func (fakeBilling) Add(_ context.Context, _, _, _ string, _, _ int) (billing.Snapshot, error) {
	return billing.Snapshot{}, nil
}

func (f fakeBilling) SessionTotal(sessionID string) billing.Snapshot { return f.snaps[sessionID] }

func (fakeBilling) DailyTotal(string) billing.Snapshot { return billing.Snapshot{} }

func runSlashREPL(t *testing.T, svc agent.Service, opt cliui.Options, input string) string {
	t.Helper()
	var buf bytes.Buffer
	opt.In = strings.NewReader(input)
	opt.Out = &buf
	opt.DisableSpinner = true
	if opt.Model == "" {
		opt.Model = "test/m"
	}
	r := cliui.NewREPL(svc, opt)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run err=%v", err)
	}
	return buf.String()
}

func TestREPL_HelpListsAllCommands(t *testing.T) {
	svc := &inputCapturingSvc{}
	got := runSlashREPL(t, svc, cliui.Options{}, "/help\n/quit\n")
	for _, want := range []string{"/help", "/model", "/compact", "/cost", "/clear", "/tools off|on", "/quit, /exit"} {
		if !strings.Contains(got, want) {
			t.Errorf("help 出力に %q が無い: %q", want, got)
		}
	}
	if len(svc.inputs) != 0 {
		t.Errorf("/help を LLM へ送っている: %d 回", len(svc.inputs))
	}
}

func TestREPL_ModelWithoutArgShowsCurrentModel(t *testing.T) {
	got := runSlashREPL(t, &inputCapturingSvc{}, cliui.Options{Model: "llama/local-7b"}, "/model\n/quit\n")
	if !strings.Contains(got, "現在のモデル: llama/local-7b") {
		t.Fatalf("現在モデルの表示なし: %q", got)
	}
	if strings.Contains(got, "利用可能:") {
		t.Errorf("AvailableModels 未設定なのに一覧を出している: %q", got)
	}
}

func TestREPL_ModelWithoutArgListsAvailableModelsSorted(t *testing.T) {
	opt := cliui.Options{
		Model: "llama/local-7b",
		AvailableModels: map[string][]string{
			"openai": {"gpt-4.1-mini", "gpt-4.1"},
			"llama":  nil,
			"anthro": {"claude"},
		},
	}
	got := runSlashREPL(t, &inputCapturingSvc{}, opt, "/model\n/quit\n")
	if !strings.Contains(got, "openai: gpt-4.1-mini, gpt-4.1") {
		t.Errorf("allow_models の一覧なし: %q", got)
	}
	if !strings.Contains(got, "llama: (allow_models 未設定 — 任意のモデル名を指定可能)") {
		t.Errorf("空 allow_models の表示なし: %q", got)
	}
	iA := strings.Index(got, "anthro:")
	iL := strings.Index(got, "llama:")
	iO := strings.Index(got, "openai:")
	if iA >= iL || iL >= iO {
		t.Errorf("プロバイダー名がソート順でない: anthro=%d llama=%d openai=%d", iA, iL, iO)
	}
}

func TestREPL_ModelSwitchAppliesToNextTurn(t *testing.T) {
	svc := &inputCapturingSvc{}
	got := runSlashREPL(t, svc, cliui.Options{Model: "llama/local-7b"}, "q1\n/model openai/gpt-4.1-mini\nq2\n/quit\n")
	if !strings.Contains(got, "次のターンから openai/gpt-4.1-mini を使用します") {
		t.Fatalf("切替メッセージなし: %q", got)
	}
	if len(svc.inputs) != 2 {
		t.Fatalf("inputs=%d, want 2", len(svc.inputs))
	}
	if svc.inputs[0].Model != "llama/local-7b" {
		t.Errorf("turn1 Model=%q, want llama/local-7b", svc.inputs[0].Model)
	}
	if svc.inputs[1].Model != "openai/gpt-4.1-mini" {
		t.Errorf("turn2 Model=%q, want openai/gpt-4.1-mini", svc.inputs[1].Model)
	}
}

func TestREPL_ModelRejectsMalformedArg(t *testing.T) {
	tests := []struct {
		name string
		arg  string
	}{
		{"スラッシュなし", "badformat"},
		{"name が空", "openai/"},
		{"provider が空", "/gpt-4.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &inputCapturingSvc{}
			got := runSlashREPL(t, svc, cliui.Options{Model: "llama/local-7b"}, "/model "+tt.arg+"\nq1\n/quit\n")
			if !strings.Contains(got, "provider/name 形式ではありません") {
				t.Fatalf("エラーメッセージなし: %q", got)
			}
			if len(svc.inputs) != 1 {
				t.Fatalf("inputs=%d, want 1", len(svc.inputs))
			}
			if svc.inputs[0].Model != "llama/local-7b" {
				t.Errorf("Model=%q, want 変更されない llama/local-7b", svc.inputs[0].Model)
			}
		})
	}
}

func TestREPL_CostWithoutBillingShowsTokensOnly(t *testing.T) {
	got := runSlashREPL(t, &tokenSvc{in: 10, out: 5}, cliui.Options{}, "q1\n/cost\n/quit\n")
	if !strings.Contains(got, "in 10 / out 5 tok") {
		t.Fatalf("トークン数の表示なし: %q", got)
	}
	if strings.Contains(got, "¥") {
		t.Errorf("Billing 未設定なのに円換算を出している: %q", got)
	}
}

func TestREPL_CostAccumulatesAcrossTurns(t *testing.T) {
	got := runSlashREPL(t, &tokenSvc{in: 10, out: 5}, cliui.Options{}, "q1\nq2\n/cost\n/quit\n")
	if !strings.Contains(got, "in 20 / out 10 tok") {
		t.Fatalf("2 ターン分の累計にならない: %q", got)
	}
}

func TestREPL_CostAccumulatesInterruptedTurn(t *testing.T) {
	// 生成結果が無く履歴を巻き戻すターンでも、届いた usage は課金済みなので積む
	got := runSlashREPL(t, usageOnlyErrorSvc{}, cliui.Options{}, "q1\n/cost\n/quit\n")
	if !strings.Contains(got, "in 7 / out 3 tok") {
		t.Fatalf("中断ターンの usage が積まれていない: %q", got)
	}
}

func TestREPL_CostWithBillingShowsJPY(t *testing.T) {
	bill := fakeBilling{snaps: map[string]billing.Snapshot{
		"s1": {InputTokens: 100, OutputTokens: 50, CostJPY: 12.34},
	}}
	opt := cliui.Options{SessionsDir: t.TempDir(), SessionID: "s1", Billing: bill}
	got := runSlashREPL(t, &tokenSvc{in: 10, out: 5}, opt, "q1\n/cost\n/quit\n")
	if !strings.Contains(got, "in 100 / out 50 tok ・ ¥12.34") {
		t.Fatalf("billing 由来の表示なし: %q", got)
	}
}

func TestREPL_ClearResetsSessionTotals(t *testing.T) {
	got := runSlashREPL(t, &tokenSvc{in: 10, out: 5}, cliui.Options{}, "q1\n/clear\n/cost\n/quit\n")
	if !strings.Contains(got, "in 0 / out 0 tok") {
		t.Fatalf("/clear が累計をリセットしていない: %q", got)
	}
}

func TestREPL_ClearResetsBillingBackedCost(t *testing.T) {
	// /clear は rec.rotate() でセッション ID を差し替えるため、billing 経路でも 0 になる
	bill := fakeBilling{snaps: map[string]billing.Snapshot{
		"s1": {InputTokens: 100, OutputTokens: 50, CostJPY: 12.34},
	}}
	opt := cliui.Options{SessionsDir: t.TempDir(), SessionID: "s1", Billing: bill}
	got := runSlashREPL(t, &tokenSvc{in: 10, out: 5}, opt, "q1\n/clear\n/cost\n/quit\n")
	if !strings.Contains(got, "in 0 / out 0 tok") {
		t.Fatalf("/clear 後も旧セッションの billing を表示している: %q", got)
	}
}

// recordingReg Resolve に渡されたモデル文字列を記録する registry のフェイク
type recordingReg struct {
	p      llm.Provider
	models []string
}

func (r *recordingReg) Resolve(model string) (llm.Provider, string, error) {
	r.models = append(r.models, model)
	return r.p, model, nil
}

func (r *recordingReg) ResolveWithFallback(model string) (llm.Provider, string, llm.Provider, string, error) {
	return r.p, model, nil, "", nil
}

func (r *recordingReg) List() []string { return []string{"fake"} }

func TestREPL_CompactUsesSwitchedModel(t *testing.T) {
	reg := &recordingReg{p: &compactProv{summary: "要約"}}
	opt := cliui.Options{
		Model:      "llama/local-7b",
		Registry:   reg,
		Compaction: cliui.CompactionOptions{Enabled: true, ContextWindowTokens: 100000, TriggerRatio: 0.9, KeepRecentTurns: 1},
	}
	runSlashREPL(t, &tokenSvc{in: 10, out: 5}, opt, "q1\n/model openai/gpt-4.1-mini\n/compact\n/quit\n")
	if len(reg.models) != 1 {
		t.Fatalf("Resolve 呼び出し=%d, want 1: %v", len(reg.models), reg.models)
	}
	if reg.models[0] != "openai/gpt-4.1-mini" {
		t.Errorf("Resolve model=%q, want 切替後の openai/gpt-4.1-mini", reg.models[0])
	}
}

func TestREPL_UnknownCommandListsAllCommands(t *testing.T) {
	got := runSlashREPL(t, &inputCapturingSvc{}, cliui.Options{}, "/foo\n/quit\n")
	if !strings.Contains(got, "/foo は未定義です") {
		t.Fatalf("未定義メッセージなし: %q", got)
	}
	for _, want := range []string{"/help", "/model", "/compact", "/cost"} {
		if !strings.Contains(got, want) {
			t.Errorf("一覧に %q が無い: %q", want, got)
		}
	}
}
