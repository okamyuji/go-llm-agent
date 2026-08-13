package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/safety"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// blockingScanner 指定語を含むテキストだけを検出するテスト用スキャナ
type blockingScanner struct {
	needle  string
	scanned []string
}

func (s *blockingScanner) Scan(text string) []safety.ScanFinding {
	s.scanned = append(s.scanned, text)
	if strings.Contains(text, s.needle) {
		return []safety.ScanFinding{{PatternID: "test-rule", Snippet: s.needle}}
	}
	return nil
}

// fixedValidator 指定回数だけ検証失敗を返すテスト用バリデータ
type fixedValidator struct {
	failures int
	calls    int
}

func (v *fixedValidator) Validate(_ string, _ json.RawMessage) (bool, string) {
	v.calls++
	if v.calls <= v.failures {
		return false, "missing required field"
	}
	return true, ""
}

// collectRun Run を実行し、送出された全イベントと戻り値のエラーを返す
func collectRun(t *testing.T, svc agent.Service, in agent.Input) ([]agent.Event, error) {
	t.Helper()
	out := make(chan agent.Event, 64)
	var events []agent.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range out {
			events = append(events, ev)
		}
	}()
	err := svc.Run(context.Background(), in, out)
	close(out)
	<-done
	return events, err
}

func TestRunReAct_ScannerBlocksUserMessage(t *testing.T) {
	sc := &blockingScanner{needle: "禁止語"}
	prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "never"}}}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil), agent.WithScanner(sc))

	events, err := collectRun(t, svc, agent.Input{
		Model:        "fake/m",
		SystemPrompt: "you are helpful",
		Messages:     []llm.Message{{Role: llm.RoleUser, Content: "これは禁止語です"}},
		MaxToolHops:  2,
	})
	if err == nil {
		t.Fatal("スキャナ検出時はエラー期待")
	}
	if !strings.Contains(err.Error(), "input blocked by safety scanner") {
		t.Fatalf("スキャナ由来のエラー期待 got %v", err)
	}
	if len(events) != 1 || events[0].Kind != agent.EventError {
		t.Fatalf("EventError 1 件期待 got %+v", events)
	}
	if prov.call != 0 {
		t.Fatalf("LLM を呼ばずに打ち切る期待 got %d", prov.call)
	}
}

func TestRunReAct_ScannerSkipsNonUserSystemRoles(t *testing.T) {
	sc := &blockingScanner{needle: "禁止語"}
	prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "ok"}}}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil), agent.WithScanner(sc))

	_, err := collectRun(t, svc, agent.Input{
		Model: "fake/m",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "安全な質問"},
			{Role: llm.RoleAssistant, Content: "禁止語を含む過去の応答"},
		},
		MaxToolHops: 2,
	})
	if err != nil {
		t.Fatalf("assistant ロールは走査対象外 got %v", err)
	}
	for _, s := range sc.scanned {
		if strings.Contains(s, "過去の応答") {
			t.Fatal("assistant メッセージを走査してはならない")
		}
	}
}

func TestRunReAct_ValidationFailureRetriesThenSucceeds(t *testing.T) {
	calls := 0
	v := &fixedValidator{failures: 1}
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{{ToolCall: &llm.ToolCall{ID: "c2", Name: "probe", Arguments: json.RawMessage(`{"a":1}`)}}},
		{{DeltaText: "done"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{namedTool{name: "probe", content: "ok", calls: &calls}}, []string{"probe"})
	svc := agent.New(fakeReg{p: prov}, tools, agent.WithValidator(v), agent.WithDefaultValidationRetries(2))

	events, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 5,
	})
	if err != nil {
		t.Fatalf("再試行で成功する期待 got %v", err)
	}
	if calls != 1 {
		t.Fatalf("検証を通った 1 回だけ実行される期待 got %d", calls)
	}
	var sawRetryResult bool
	for _, ev := range events {
		if ev.Kind == agent.EventToolResult && strings.Contains(ev.ToolResult.Content, "schema validation failed") {
			sawRetryResult = true
			if !ev.ToolResult.IsError {
				t.Fatal("検証失敗のツール結果は IsError 期待")
			}
		}
	}
	if !sawRetryResult {
		t.Fatal("検証失敗を伝えるツール結果が履歴へ積まれる期待")
	}
}

func TestRunReAct_ValidationRetriesExhausted(t *testing.T) {
	calls := 0
	v := &fixedValidator{failures: 10}
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
	}}
	tools := tool.NewRegistry([]tool.Tool{namedTool{name: "probe", content: "ok", calls: &calls}}, []string{"probe"})
	svc := agent.New(fakeReg{p: prov}, tools, agent.WithValidator(v))

	_, err := collectRun(t, svc, agent.Input{
		Model:                "fake/m",
		Messages:             []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops:          5,
		ValidationMaxRetries: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "schema validation max retries (1) exceeded") {
		t.Fatalf("budget 超過エラー期待 got %v", err)
	}
	if calls != 0 {
		t.Fatalf("検証を通らないツールは実行されない期待 got %d", calls)
	}
}

func TestRunReAct_ValidationRetriesDisabled(t *testing.T) {
	v := &fixedValidator{failures: 10}
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
	}}
	tools := tool.NewRegistry([]tool.Tool{namedTool{name: "probe", content: "ok"}}, []string{"probe"})
	svc := agent.New(fakeReg{p: prov}, tools, agent.WithValidator(v))

	_, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 5,
	})
	if err == nil || !strings.Contains(err.Error(), "retries disabled") {
		t.Fatalf("retries disabled エラー期待 got %v", err)
	}
}

func TestRunReAct_ValidationBudgetResetsPerCallID(t *testing.T) {
	calls := 0
	// 呼び出しごとに 1 回失敗する。budget が per-call で初期化されなければ
	// 2 件目の ToolCall で budget 超過エラーになる
	v := &alternatingValidator{}
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{{ToolCall: &llm.ToolCall{ID: "c2", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{{ToolCall: &llm.ToolCall{ID: "c2", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{{DeltaText: "done"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{namedTool{name: "probe", content: "ok", calls: &calls}}, []string{"probe"})
	svc := agent.New(fakeReg{p: prov}, tools, agent.WithValidator(v), agent.WithDefaultValidationRetries(1))

	_, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 8,
	})
	if err != nil {
		t.Fatalf("CallID ごとに budget が初期化される期待 got %v", err)
	}
	if calls != 2 {
		t.Fatalf("2 件の ToolCall が実行される期待 got %d", calls)
	}
}

// alternatingValidator 奇数回目の Validate だけ失敗するテスト用バリデータ
type alternatingValidator struct{ calls int }

func (v *alternatingValidator) Validate(_ string, _ json.RawMessage) (bool, string) {
	v.calls++
	if v.calls%2 == 1 {
		return false, "odd call fails"
	}
	return true, ""
}

func TestRunReAct_UnknownToolAbortsTurn(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "missing", Arguments: json.RawMessage(`{}`)}}},
	}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))

	_, err := collectRun(t, svc, agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 3,
	})
	if err == nil || !strings.Contains(err.Error(), "が見つかりません") {
		t.Fatalf("未登録ツールのエラー期待 got %v", err)
	}
}
