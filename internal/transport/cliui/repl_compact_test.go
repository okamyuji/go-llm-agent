package cliui_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// usageSvc ターンごとに指定した InputTokens の EventUsage を返すフェイク
type usageSvc struct {
	usages [][]int
	turn   int
}

func (s *usageSvc) Run(_ context.Context, _ agent.Input, out chan<- agent.Event) error {
	var inputs []int
	if s.turn < len(s.usages) {
		inputs = s.usages[s.turn]
	}
	s.turn++
	for _, in := range inputs {
		out <- agent.Event{Kind: agent.EventUsage, Usage: &llm.Usage{InputTokens: in, OutputTokens: 1}}
	}
	out <- agent.Event{Kind: agent.EventDelta, Delta: "answer"}
	final := llm.Message{Role: llm.RoleAssistant, Content: "answer"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final, TurnMessages: []llm.Message{final}}
	return nil
}

// compactProv 圧縮の要約呼び出しに応じるフェイク provider
type compactProv struct {
	summary string
	err     error
	delay   time.Duration
	calls   int
}

func (p *compactProv) Name() string { return "fake" }

func (p *compactProv) Chat(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	if p.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(p.delay):
		}
	}
	if p.err != nil {
		return nil, p.err
	}
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: p.summary}}, nil
}

func (p *compactProv) Stream(_ context.Context, _ llm.ChatRequest) (llm.ChatStream, error) {
	return nil, errors.New("stream は使わない")
}

// compactReg Resolve が成功または失敗する registry のフェイク
type compactReg struct {
	p   llm.Provider
	err error
}

func (r compactReg) Resolve(model string) (llm.Provider, string, error) {
	if r.err != nil {
		return nil, "", r.err
	}
	return r.p, model, nil
}

func (r compactReg) ResolveWithFallback(model string) (llm.Provider, string, llm.Provider, string, error) {
	return r.p, model, nil, "", r.err
}

func (r compactReg) List() []string { return []string{"fake"} }

func runCompactREPL(t *testing.T, svc agent.Service, reg llm.Registry, opts cliui.CompactionOptions, input string) string {
	t.Helper()
	var buf bytes.Buffer
	r := cliui.NewREPL(svc, cliui.Options{
		Model:          "fake/m",
		In:             strings.NewReader(input),
		Out:            &buf,
		DisableSpinner: true,
		Registry:       reg,
		Compaction:     opts,
	})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func autoOptions() cliui.CompactionOptions {
	return cliui.CompactionOptions{Enabled: true, ContextWindowTokens: 100, TriggerRatio: 0.5, KeepRecentTurns: 1}
}

func TestREPL_AutoCompactionTriggersWhenThresholdExceeded(t *testing.T) {
	prov := &compactProv{summary: "要約"}
	svc := &usageSvc{usages: [][]int{{10}, {60}}}
	got := runCompactREPL(t, svc, compactReg{p: prov}, autoOptions(), "q1\nq2\n")
	if !strings.Contains(got, "[compact] 会話履歴を圧縮しました") {
		t.Fatalf("自動発火期待 got %q", got)
	}
	if prov.calls != 1 {
		t.Fatalf("要約呼び出し 1 回期待 got %d", prov.calls)
	}
}

func TestREPL_AutoCompactionSkippedWhenBelowThreshold(t *testing.T) {
	prov := &compactProv{summary: "要約"}
	svc := &usageSvc{usages: [][]int{{10}, {40}}}
	got := runCompactREPL(t, svc, compactReg{p: prov}, autoOptions(), "q1\nq2\n")
	if strings.Contains(got, "[compact]") {
		t.Fatalf("閾値未満では発火しない期待 got %q", got)
	}
	if prov.calls != 0 {
		t.Fatalf("要約を呼ばない期待 got %d", prov.calls)
	}
}

func TestREPL_CompactionThresholdUsesLastUsageEvent(t *testing.T) {
	// 積算 80 でも最後が 20 なら閾値 50 を下回るため発火しない
	provDesc := &compactProv{summary: "要約"}
	gotDesc := runCompactREPL(t, &usageSvc{usages: [][]int{{10}, {60, 20}}}, compactReg{p: provDesc}, autoOptions(), "q1\nq2\n")
	if strings.Contains(gotDesc, "[compact]") {
		t.Fatalf("最後の usage で判定する期待 got %q", gotDesc)
	}
	provAsc := &compactProv{summary: "要約"}
	gotAsc := runCompactREPL(t, &usageSvc{usages: [][]int{{10}, {20, 60}}}, compactReg{p: provAsc}, autoOptions(), "q1\nq2\n")
	if !strings.Contains(gotAsc, "[compact] 会話履歴を圧縮しました") {
		t.Fatalf("最後が閾値超なら発火する期待 got %q", gotAsc)
	}
}

func TestREPL_CompactCommand_TriggersManually(t *testing.T) {
	prov := &compactProv{summary: "要約"}
	opts := autoOptions()
	opts.TriggerRatio = 1.0 // 自動発火しない設定
	got := runCompactREPL(t, &usageSvc{usages: [][]int{{10}, {10}}}, compactReg{p: prov}, opts, "q1\nq2\n/compact\n")
	if !strings.Contains(got, "[compact] 会話履歴を圧縮しました") {
		t.Fatalf("手動発火期待 got %q", got)
	}
	if prov.calls != 1 {
		t.Fatalf("要約呼び出し 1 回期待 got %d", prov.calls)
	}
}

func TestREPL_CompactCommand_NoRegistry_ShowsWarning(t *testing.T) {
	var buf bytes.Buffer
	r := cliui.NewREPL(&usageSvc{}, cliui.Options{
		Model:          "fake/m",
		In:             strings.NewReader("/compact\n"),
		Out:            &buf,
		DisableSpinner: true,
	})
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[compact] Registry が未設定のため圧縮できません") {
		t.Fatalf("未設定の警告期待 got %q", buf.String())
	}
}

func TestREPL_CompactionFailure_ContinuesWithOriginalHistory(t *testing.T) {
	opts := autoOptions()
	opts.TriggerRatio = 1.0
	got := runCompactREPL(t, &usageSvc{usages: [][]int{{10}, {10}}},
		compactReg{err: errors.New("no such model")}, opts, "q1\n/compact\nq2\n")
	if !strings.Contains(got, "[compact] 警告: モデル解決に失敗したため圧縮をスキップしました") {
		t.Fatalf("解決失敗の警告期待 got %q", got)
	}
	// 圧縮失敗後もターンが継続する
	if strings.Count(got, "answer") < 2 {
		t.Fatalf("後続ターンが継続する期待 got %q", got)
	}
}

func TestREPL_CompactionSummarizerError_ContinuesWithOriginalHistory(t *testing.T) {
	opts := autoOptions()
	opts.TriggerRatio = 1.0
	got := runCompactREPL(t, &usageSvc{usages: [][]int{{10}, {10}, {10}}},
		compactReg{p: &compactProv{err: errors.New("boom")}}, opts, "q1\nq2\n/compact\nq3\n")
	if !strings.Contains(got, "[compact] 警告: 履歴圧縮に失敗したため元の履歴のまま続行します") {
		t.Fatalf("要約失敗の警告期待 got %q", got)
	}
}

func TestREPL_CompactHistory_NoOpReportsNothingToCompact(t *testing.T) {
	prov := &compactProv{summary: "要約"}
	opts := autoOptions()
	opts.TriggerRatio = 1.0
	opts.KeepRecentTurns = 4
	got := runCompactREPL(t, &usageSvc{usages: [][]int{{10}}}, compactReg{p: prov}, opts, "q1\n/compact\n")
	if !strings.Contains(got, "[compact] 圧縮対象がありません") {
		t.Fatalf("no-op 表示期待 got %q", got)
	}
	if strings.Contains(got, "[compact] 会話履歴を圧縮しました") {
		t.Fatalf("圧縮したと表示しない期待 got %q", got)
	}
	if prov.calls != 0 {
		t.Fatalf("要約を呼ばない期待 got %d", prov.calls)
	}
}

func TestREPL_CompactionTimeout_SkipsAndContinues(t *testing.T) {
	restore := cliui.SetCompactTimeoutForTest(50 * time.Millisecond)
	t.Cleanup(restore)
	opts := autoOptions()
	opts.TriggerRatio = 1.0
	got := runCompactREPL(t, &usageSvc{usages: [][]int{{10}, {10}, {10}}},
		compactReg{p: &compactProv{summary: "要約", delay: 2 * time.Second}}, opts, "q1\nq2\n/compact\nq3\n")
	if !strings.Contains(got, "[compact] 警告: 圧縮が") {
		t.Fatalf("上限超過の警告期待 got %q", got)
	}
	if strings.Count(got, "answer") < 3 {
		t.Fatalf("後続ターンが継続する期待 got %q", got)
	}
}

func TestREPL_CompactionInterruptedByESC(t *testing.T) {
	prov := &compactProv{summary: "要約", delay: 3 * time.Second}
	opts := autoOptions()
	opts.TriggerRatio = 1.0
	pr, pw := io.Pipe()
	out := newMarkerWriter("[compact] 会話履歴を圧縮しています")
	r := cliui.NewREPL(&usageSvc{usages: [][]int{{10}, {10}, {10}}}, cliui.Options{
		Model:          "fake/m",
		In:             pr,
		Out:            out,
		DisableSpinner: true,
		Registry:       compactReg{p: prov},
		Compaction:     opts,
	})
	go func() {
		_, _ = pw.Write([]byte("q1\nq2\n/compact\n"))
		select {
		case <-out.ch:
		case <-time.After(3 * time.Second):
		}
		_, _ = pw.Write([]byte("\x1b"))
		_, _ = pw.Write([]byte("q3\n"))
		_ = pw.Close()
	}()
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = pr.Close()
	got := out.String()
	if !strings.Contains(got, "[compact] 中断しました。元の履歴のまま続行します") {
		t.Fatalf("中断表示期待 got %q", got)
	}
	if strings.Contains(got, "[compact] 会話履歴を圧縮しました") {
		t.Fatalf("中断時は圧縮しない期待 got %q", got)
	}
	if strings.Count(got, "answer") < 3 {
		t.Fatalf("中断後もターンが継続する期待 got %q", got)
	}
}
