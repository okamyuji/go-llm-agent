package agent_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// TestMain パッケージ全テスト終了時に goroutine リークを検証する
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type fakeStream struct {
	events []llm.StreamEvent
	i      int
}

func (f *fakeStream) Recv() (llm.StreamEvent, bool) {
	if f.i >= len(f.events) {
		return llm.StreamEvent{}, false
	}
	ev := f.events[f.i]
	f.i++
	return ev, true
}
func (f *fakeStream) Close() error { return nil }

type fakeProvider struct {
	streams  [][]llm.StreamEvent
	call     int
	requests []llm.ChatRequest
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, nil
}
func (f *fakeProvider) Stream(_ context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	f.requests = append(f.requests, req)
	ev := f.streams[f.call]
	f.call++
	return &fakeStream{events: ev}, nil
}

type fakeReg struct{ p llm.Provider }

func (f fakeReg) Resolve(model string) (llm.Provider, string, error) { return f.p, model, nil }
func (f fakeReg) ResolveWithFallback(model string) (llm.Provider, string, llm.Provider, string, error) {
	return f.p, model, nil, "", nil
}
func (f fakeReg) List() []string { return []string{"fake"} }

type echoTool struct{}

func (e echoTool) Spec() tool.Spec {
	return tool.Spec{Name: "echo", Description: "", Schema: json.RawMessage(`{}`)}
}
func (e echoTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: string(raw)}, nil
}

type namedTool struct {
	name    string
	content string
	isError bool
	calls   *int
	args    *[]json.RawMessage
}

func (t namedTool) Spec() tool.Spec {
	return tool.Spec{Name: t.name, Description: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}

func (t namedTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	if t.calls != nil {
		(*t.calls)++
	}
	if t.args != nil {
		*t.args = append(*t.args, append(json.RawMessage(nil), raw...))
	}
	return tool.Result{Content: t.content, IsError: t.isError}, nil
}

func TestRun_BasicToolLoop(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{
			{ToolCall: &llm.ToolCall{ID: "c1", Name: "echo", Arguments: json.RawMessage(`"ping"`)}},
		},
		{
			{DeltaText: "done"},
		},
	}}
	reg := fakeReg{p: prov}
	tools := tool.NewRegistry([]tool.Tool{echoTool{}}, []string{"echo"})
	svc := agent.New(reg, tools)

	out := make(chan agent.Event, 16)
	done := make(chan struct{})
	go func() {
		_ = svc.Run(context.Background(), agent.Input{
			Model:        "fake/m",
			Messages:     []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
			MaxToolHops:  3,
			EnabledTools: []string{"echo"},
		}, out)
		close(out)
		close(done)
	}()
	var kinds []agent.EventKind
	for ev := range out {
		kinds = append(kinds, ev.Kind)
	}
	<-done
	if len(kinds) < 3 {
		t.Fatalf("少なくとも 3 イベント期待, got %v", kinds)
	}
	if kinds[len(kinds)-1] != agent.EventFinal {
		t.Fatalf("最後は Final 期待 got %v", kinds[len(kinds)-1])
	}
}

func TestRun_ToolChoiceNoneSuppressesToolAdvertisement(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "answer"}},
	}}
	reg := fakeReg{p: prov}
	tools := tool.NewRegistry([]tool.Tool{echoTool{}}, []string{"echo"})
	svc := agent.New(reg, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 3,
		ToolChoice:  &llm.ToolChoice{Mode: "none"},
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(prov.requests))
	}
	if len(prov.requests[0].Tools) != 0 {
		t.Errorf("tool_choice none should suppress tool advertisement, got %d tools", len(prov.requests[0].Tools))
	}
	if prov.requests[0].ToolChoice != nil {
		t.Errorf("tool_choice should be nil when tools are suppressed, got %+v", prov.requests[0].ToolChoice)
	}
}

func TestRun_ToolChoiceNoneSuppressesWebRequirementForCurrentInfo(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "ツールなしの回答"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{
		namedTool{name: "web_search"},
		namedTool{name: "web_fetch"},
	}, []string{"web_search", "web_fetch"})
	svc := agent.New(fakeReg{p: prov}, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "今日のニュースを教えて"}},
		MaxToolHops: 1,
		ToolChoice:  &llm.ToolChoice{Mode: "none"},
	}, out)
	close(out)
	for range out {
	}
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("requests=%d, want 1", len(prov.requests))
	}
	if len(prov.requests[0].Tools) != 0 {
		t.Errorf("tools=%d, want 0", len(prov.requests[0].Tools))
	}
	if prov.requests[0].ToolChoice != nil {
		t.Errorf("ToolChoice=%+v, want nil", prov.requests[0].ToolChoice)
	}
}

func TestRun_DefaultToolChoiceNoneSuppressesTools(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "answer"}},
	}}
	reg := fakeReg{p: prov}
	tools := tool.NewRegistry([]tool.Tool{echoTool{}}, []string{"echo"})
	svc := agent.New(reg, tools, agent.WithDefaultToolChoice(&llm.ToolChoice{Mode: "none"}))

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 3,
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if len(prov.requests[0].Tools) != 0 {
		t.Errorf("default tool_choice none should suppress tools, got %d tools", len(prov.requests[0].Tools))
	}
}

func TestRun_MaxHopsExceeded(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)}}},
		{{ToolCall: &llm.ToolCall{ID: "c2", Name: "echo", Arguments: json.RawMessage(`{}`)}}},
	}}
	reg := fakeReg{p: prov}
	tools := tool.NewRegistry([]tool.Tool{echoTool{}}, []string{"echo"})
	svc := agent.New(reg, tools)

	out := make(chan agent.Event, 64)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		MaxToolHops: 0,
	}, out)
	close(out)
	if err == nil {
		t.Fatal("MaxToolHops 超過でエラー")
	}
}

func TestRun_RequiresWebSearchThenFetchForCurrentInfo(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "Goの最新情報です"}}}}
	var searchCalls, fetchCalls int
	var searchArgs, fetchArgs []json.RawMessage
	tools := tool.NewRegistry([]tool.Tool{
		namedTool{
			name:    "web_search",
			content: `{"results":[{"url":"https://r1999.com/version/golang-version/"},{"url":"https://go.dev/doc/devel/release"}]}`,
			calls:   &searchCalls,
			args:    &searchArgs,
		},
		namedTool{name: "web_fetch", content: "Go releases", calls: &fetchCalls, args: &fetchArgs},
	}, []string{"web_search", "web_fetch"})
	svc := agent.New(fakeReg{p: prov}, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:        "fake/m",
		Messages:     []llm.Message{{Role: llm.RoleUser, Content: "Goの最新安定版を公式情報で教えて"}},
		MaxToolHops:  3,
		EnabledTools: []string{"web_search", "web_fetch"},
	}, out)
	close(out)
	for range out {
	}
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if searchCalls != 1 || fetchCalls != 1 {
		t.Fatalf("searchCalls=%d fetchCalls=%d, want 1 each", searchCalls, fetchCalls)
	}
	if len(searchArgs) != 1 || !strings.Contains(string(searchArgs[0]), "Goの最新安定版を公式情報で教えて") {
		t.Errorf("search args=%s, want user prompt as query", searchArgs)
	}
	if len(fetchArgs) != 1 || !strings.Contains(string(fetchArgs[0]), `"url":"https://go.dev/doc/devel/release"`) {
		t.Errorf("fetch args=%s, want official go.dev URL", fetchArgs)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("requests=%d, want 1", len(prov.requests))
	}
	gotMessages := prov.requests[0].Messages
	if len(gotMessages) != 5 {
		t.Fatalf("LLM messages=%d, want 5: %+v", len(gotMessages), gotMessages)
	}
	if gotMessages[1].Role != llm.RoleAssistant || len(gotMessages[1].ToolCalls) != 1 || gotMessages[1].ToolCalls[0].Name != "web_search" {
		t.Errorf("messages[1]=%+v, want web_search tool call", gotMessages[1])
	}
	if gotMessages[2].Role != llm.RoleTool || gotMessages[2].Name != "web_search" {
		t.Errorf("messages[2]=%+v, want web_search result", gotMessages[2])
	}
	if gotMessages[3].Role != llm.RoleAssistant || len(gotMessages[3].ToolCalls) != 1 || gotMessages[3].ToolCalls[0].Name != "web_fetch" {
		t.Errorf("messages[3]=%+v, want web_fetch tool call", gotMessages[3])
	}
	if gotMessages[4].Role != llm.RoleTool || gotMessages[4].Name != "web_fetch" {
		t.Errorf("messages[4]=%+v, want web_fetch result", gotMessages[4])
	}
}

func TestRun_AutomaticWebFlowRetriesRawToolCallTextBeforeEmittingAnswer(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: ` [TOOL_CALLS][{"name":"web_fetch","arguments":{"url":"https://example.com/other"}}] `}},
		{{DeltaText: "Go 1.26.5です。公式URLは https://go.dev/ です。"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{
		namedTool{name: "web_search", content: `{"results":[{"url":"https://go.dev/"}]}`},
		namedTool{name: "web_fetch", content: "Go 1.26.5 release information"},
	}, []string{"web_search", "web_fetch"})
	svc := agent.New(fakeReg{p: prov}, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "Goの最新安定版をWeb検索して"}},
		MaxToolHops: 3,
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	var deltas strings.Builder
	var final *llm.Message
	for event := range out {
		if event.Kind == agent.EventDelta {
			deltas.WriteString(event.Delta)
		}
		if event.Kind == agent.EventFinal {
			final = event.Final
		}
	}
	if len(prov.requests) != 2 {
		t.Fatalf("requests=%d, want one retry", len(prov.requests))
	}
	if prov.requests[0].Temperature != nil || prov.requests[1].Temperature == nil || *prov.requests[1].Temperature != 0.3 {
		t.Errorf("retry temperatures=(%v, %v), want (nil, 0.3)", prov.requests[0].Temperature, prov.requests[1].Temperature)
	}
	if final == nil {
		t.Fatal("EventFinal not emitted")
	}
	if strings.Contains(deltas.String(), "TOOL_CALLS") || strings.Contains(final.Content, "TOOL_CALLS") {
		t.Errorf("raw tool call text was emitted: deltas=%q final=%q", deltas.String(), final.Content)
	}
	if deltas.String() != final.Content || !strings.Contains(final.Content, "Go 1.26.5") {
		t.Errorf("deltas=%q final=%q, want only complete answer", deltas.String(), final.Content)
	}
}

func TestRun_AutomaticWebToolsAreWithheldOnlyFromFirstLLMRequest(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "echo1", Name: "echo", Arguments: json.RawMessage(`{"value":"x"}`)}}},
		{{DeltaText: "done"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{
		namedTool{name: "web_search", content: `{"results":[{"url":"https://example.com/"}]}`},
		namedTool{name: "web_fetch", content: "body"},
		echoTool{},
	}, []string{"web_search", "web_fetch", "echo"})
	svc := agent.New(fakeReg{p: prov}, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "今日のニュースをWeb検索して"}},
		MaxToolHops: 3,
	}, out)
	close(out)
	for range out {
	}
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if len(prov.requests) != 2 {
		t.Fatalf("requests=%d, want 2", len(prov.requests))
	}
	if len(prov.requests[0].Tools) != 0 {
		t.Errorf("first request tools=%d, want 0 after automatic web execution", len(prov.requests[0].Tools))
	}
	if len(prov.requests[1].Tools) != 3 {
		t.Errorf("second request tools=%d, want all 3 tools restored", len(prov.requests[1].Tools))
	}
	if prov.requests[1].ToolChoice != nil {
		t.Errorf("second request ToolChoice=%+v, want nil to avoid forcing another web search", prov.requests[1].ToolChoice)
	}
}

func TestRun_AutomaticWebToolCallIDsAreUniqueAcrossTurns(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "first answer"}},
		{{DeltaText: "second answer"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{
		namedTool{name: "web_search", content: `{"results":[{"url":"https://example.com/"}]}`},
		namedTool{name: "web_fetch", content: "body"},
	}, []string{"web_search", "web_fetch"})
	svc := agent.New(fakeReg{p: prov}, tools)

	history := []llm.Message{{Role: llm.RoleUser, Content: "今日のニュースをWeb検索して"}}
	firstEvents := make(chan agent.Event, 16)
	if err := svc.Run(context.Background(), agent.Input{Model: "fake/m", Messages: history, MaxToolHops: 3}, firstEvents); err != nil {
		t.Fatalf("first run err=%v", err)
	}
	close(firstEvents)
	for event := range firstEvents {
		if event.Kind == agent.EventFinal {
			history = append(history, event.TurnMessages...)
		}
	}
	history = append(history, llm.Message{Role: llm.RoleUser, Content: "現在の天気もWeb検索して"})
	secondEvents := make(chan agent.Event, 16)
	if err := svc.Run(context.Background(), agent.Input{Model: "fake/m", Messages: history, MaxToolHops: 3}, secondEvents); err != nil {
		t.Fatalf("second run err=%v", err)
	}
	close(secondEvents)
	for range secondEvents {
	}

	if len(prov.requests) != 2 {
		t.Fatalf("requests=%d, want 2", len(prov.requests))
	}
	firstSearchID := prov.requests[0].Messages[1].ToolCalls[0].ID
	firstFetchID := prov.requests[0].Messages[3].ToolCalls[0].ID
	secondMessages := prov.requests[1].Messages
	secondSearchID := secondMessages[len(secondMessages)-4].ToolCalls[0].ID
	secondFetchID := secondMessages[len(secondMessages)-2].ToolCalls[0].ID
	ids := []string{firstSearchID, firstFetchID, secondSearchID, secondFetchID}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if len(id) != 9 {
			t.Errorf("tool call ID %q has length %d, want 9", id, len(id))
		}
		for _, r := range id {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
				t.Errorf("tool call ID %q is not alphanumeric", id)
			}
		}
		if _, duplicate := seen[id]; duplicate {
			t.Errorf("duplicate tool call ID %q in %v", id, ids)
		}
		seen[id] = struct{}{}
	}
}

func TestRun_DoesNotRequireWebFetchAfterSearchFailure(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "検索に失敗しました"}}}}
	var searchCalls, fetchCalls int
	tools := tool.NewRegistry([]tool.Tool{
		namedTool{name: "web_search", content: "network error", isError: true, calls: &searchCalls},
		namedTool{name: "web_fetch", content: "unused", calls: &fetchCalls},
	}, []string{"web_search", "web_fetch"})
	svc := agent.New(fakeReg{p: prov}, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "今日の天気を教えて"}},
		MaxToolHops: 2,
	}, out)
	close(out)
	for range out {
	}
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if searchCalls != 1 || fetchCalls != 0 {
		t.Fatalf("searchCalls=%d fetchCalls=%d, want 1 and 0", searchCalls, fetchCalls)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("requests=%d, want 1", len(prov.requests))
	}
	if got := prov.requests[0].ToolChoice; got != nil {
		t.Errorf("ToolChoice=%+v, want nil", got)
	}
}

func TestRun_OrdinaryPromptKeepsAutomaticToolChoice(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "説明します"}}}}
	var searchCalls, fetchCalls int
	tools := tool.NewRegistry([]tool.Tool{
		namedTool{name: "web_search", calls: &searchCalls},
		namedTool{name: "web_fetch", calls: &fetchCalls},
	}, []string{"web_search", "web_fetch"})
	svc := agent.New(fakeReg{p: prov}, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "Explain concurrent map and data conversion"}},
		MaxToolHops: 1,
	}, out)
	close(out)
	for range out {
	}
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if got := prov.requests[0].ToolChoice; got != nil {
		t.Errorf("ToolChoice=%+v, want nil", got)
	}
	if searchCalls != 0 || fetchCalls != 0 {
		t.Errorf("searchCalls=%d fetchCalls=%d, want 0 each", searchCalls, fetchCalls)
	}
}

func TestRun_LocalFileSearchDoesNotExecuteWebTools(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "ローカルを確認します"}}}}
	var searchCalls, fetchCalls int
	tools := tool.NewRegistry([]tool.Tool{
		namedTool{name: "web_search", calls: &searchCalls},
		namedTool{name: "web_fetch", calls: &fetchCalls},
	}, []string{"web_search", "web_fetch"})
	svc := agent.New(fakeReg{p: prov}, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "リポジトリ内のバージョン文字列を検索して"}},
		MaxToolHops: 1,
	}, out)
	close(out)
	for range out {
	}
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if searchCalls != 0 || fetchCalls != 0 {
		t.Errorf("searchCalls=%d fetchCalls=%d, want 0 each", searchCalls, fetchCalls)
	}
}

func TestRun_TimeWordsWithoutExternalContextDoNotExecuteWebTools(t *testing.T) {
	for _, prompt := range []string{
		"現在のコードのバグを教えて",
		"今日追加した関数をリファクタリングして",
		"この時点の変数をログ出力して",
	} {
		t.Run(prompt, func(t *testing.T) {
			prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "確認します"}}}}
			var searchCalls int
			tools := tool.NewRegistry([]tool.Tool{
				namedTool{name: "web_search", calls: &searchCalls},
			}, []string{"web_search"})
			svc := agent.New(fakeReg{p: prov}, tools)

			out := make(chan agent.Event, 16)
			err := svc.Run(context.Background(), agent.Input{
				Model:       "fake/m",
				Messages:    []llm.Message{{Role: llm.RoleUser, Content: prompt}},
				MaxToolHops: 1,
			}, out)
			close(out)
			for range out {
			}
			if err != nil {
				t.Fatalf("run err=%v", err)
			}
			if searchCalls != 0 {
				t.Errorf("searchCalls=%d, want 0", searchCalls)
			}
		})
	}
}

func TestRun_CurrentInfoWithoutWebSearchToolDoesNotExecuteWebTools(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "利用可能な範囲で回答します"}}}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "今日のニュースを教えて"}},
		MaxToolHops: 1,
	}, out)
	close(out)
	for range out {
	}
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if len(prov.requests) != 1 || len(prov.requests[0].Tools) != 0 {
		t.Errorf("requests=%+v, want one request without tools", prov.requests)
	}
}

func TestRun_FollowUpDetailDoesNotInjectPromptIntoMessages(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "理由: コンパイル時に型を検査します。背景: 実行前に不整合を発見できます。"}}}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:        "fake/m",
		SystemPrompt: "日本語で答える",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Goの特徴を教えて"},
			{Role: llm.RoleAssistant, Content: "静的型付けです"},
			{Role: llm.RoleUser, Content: "もう少し詳しく。"},
		},
		MaxToolHops: 1,
	}, out)
	close(out)
	for range out {
	}
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if len(prov.requests) != 1 || len(prov.requests[0].Messages) == 0 {
		t.Fatalf("requests=%+v", prov.requests)
	}
	system := prov.requests[0].Messages[0]
	if system.Role != llm.RoleSystem {
		t.Fatalf("first message role=%q, want system", system.Role)
	}
	if system.Content != "日本語で答える" {
		t.Errorf("system prompt was modified: %q", system.Content)
	}
}

func TestRun_FollowUpDetailFiltersRepeatedContentAndRetries(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "2026年8月10日時点のGo言語の最新安定版は、以下の通りです。\n公式URLは https://go.dev/ です。"}},
		{{DeltaText: "2026年8月時点でのGo言語の最新安定版です。\n理由: 2026年7月7日にリリースされました。背景: コンパイラとランタイムの修正を含みます。"}},
	}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model: "fake/m",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Goの最新版を教えて"},
			{Role: llm.RoleAssistant, Content: "2026年8月時点でのGo言語の最新安定版はGo 1.26.5です。公式URLは https://go.dev/ です。"},
			{Role: llm.RoleUser, Content: "もう少し詳しく。"},
		},
		MaxToolHops: 0,
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	var deltas strings.Builder
	var final *llm.Message
	for event := range out {
		if event.Kind == agent.EventDelta {
			deltas.WriteString(event.Delta)
		}
		if event.Kind == agent.EventFinal {
			final = event.Final
		}
	}
	if len(prov.requests) != 2 {
		t.Fatalf("requests=%d, want one retry", len(prov.requests))
	}
	if !reflect.DeepEqual(prov.requests[0].Messages, prov.requests[1].Messages) {
		t.Errorf("retry injected messages: first=%+v second=%+v", prov.requests[0].Messages, prov.requests[1].Messages)
	}
	if prov.requests[0].Temperature != nil || prov.requests[1].Temperature == nil || *prov.requests[1].Temperature != 0.3 {
		t.Errorf("retry temperatures=(%v, %v), want (nil, 0.3)", prov.requests[0].Temperature, prov.requests[1].Temperature)
	}
	if final == nil {
		t.Fatal("EventFinal not emitted")
	}
	for _, repeated := range []string{"Go言語の最新安定版", "https://go.dev/"} {
		if strings.Contains(final.Content, repeated) || strings.Contains(deltas.String(), repeated) {
			t.Errorf("repeated content %q was emitted: final=%q deltas=%q", repeated, final.Content, deltas.String())
		}
	}
	if !strings.Contains(final.Content, "2026年7月7日") || deltas.String() != final.Content {
		t.Errorf("final=%q deltas=%q, want only novel release detail", final.Content, deltas.String())
	}
	for _, punctuated := range []string{"リリースされました。", "修正を含みます。"} {
		if !strings.Contains(final.Content, punctuated) {
			t.Errorf("sentence delimiter missing from %q", final.Content)
		}
	}
	for _, incompleteEnding := range []string{"で\n", "おり\n"} {
		if strings.Contains(final.Content+"\n", incompleteEnding) {
			t.Errorf("final contains incomplete clause ending %q: %q", incompleteEnding, final.Content)
		}
	}
}

func TestRun_FollowUpDetailRejectsPromptFragmentsNotGroundedInWebFetch(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "[人称・敬語]\n使用する言語は日本語のみ。\n態度は謙虚で穏やか。"}},
		{{DeltaText: "理由: 2026年7月7日にリリースされました。背景: コンパイラとランタイムの不具合修正を含みます。"}},
	}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))
	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model: "fake/m",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Goの最新版を教えて"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "webf00001", Name: "web_fetch"}}},
			{Role: llm.RoleTool, Name: "web_fetch", ToolCallID: "webf00001", Content: "[UNTRUSTED INPUT: tool=web_fetch]\n2026年7月7日にリリースされました。コンパイラとランタイムの不具合修正を含みます。\n[END UNTRUSTED]"},
			{Role: llm.RoleAssistant, Content: "Go 1.26.5です。公式URLは https://go.dev/ です。"},
			{Role: llm.RoleUser, Content: "もう少し詳しく。"},
		},
		MaxToolHops: 0,
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	var final *llm.Message
	for event := range out {
		if event.Kind == agent.EventFinal {
			final = event.Final
		}
	}
	if len(prov.requests) != 2 {
		t.Fatalf("requests=%d, want one retry after ungrounded prompt fragments", len(prov.requests))
	}
	if final == nil {
		t.Fatal("EventFinal not emitted")
	}
	for _, leaked := range []string{"人称", "敬語", "使用する言語", "態度は"} {
		if strings.Contains(final.Content, leaked) {
			t.Errorf("prompt fragment %q was emitted: %q", leaked, final.Content)
		}
	}
	for _, grounded := range []string{"2026年7月7日", "コンパイラとランタイム"} {
		if !strings.Contains(final.Content, grounded) {
			t.Errorf("grounded detail %q missing from %q", grounded, final.Content)
		}
	}
}

func TestRun_FollowUpDetailRejectsNonJapaneseCandidateForJapaneseConversation(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "Compiler and runtime fixes are included. Security patches are also included."}},
		{{DeltaText: "コンパイラとランタイムの修正を含みます。\n+ 暗号処理の安全性も改善されています。"}},
	}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))
	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model: "fake/m",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Goの特徴を教えて"},
			{Role: llm.RoleAssistant, Content: "静的型付けです。"},
			{Role: llm.RoleUser, Content: "もう少し詳しく。"},
		},
		MaxToolHops: 0,
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	var final *llm.Message
	for event := range out {
		if event.Kind == agent.EventFinal {
			final = event.Final
		}
	}
	if len(prov.requests) != 2 {
		t.Fatalf("requests=%d, want one retry after non-Japanese candidate", len(prov.requests))
	}
	if final == nil {
		t.Fatal("EventFinal not emitted")
	}
	if strings.Contains(final.Content, "Compiler") || strings.Contains(final.Content, "Security") {
		t.Errorf("non-Japanese candidate was emitted: %q", final.Content)
	}
	if strings.Contains(final.Content, "\n- +") {
		t.Errorf("nested list marker was emitted: %q", final.Content)
	}
	for _, detail := range []string{"コンパイラとランタイム", "暗号処理の安全性"} {
		if !strings.Contains(final.Content, detail) {
			t.Errorf("Japanese detail %q missing from %q", detail, final.Content)
		}
	}
}

func TestRun_FollowUpDetailRejectsIntroductionOnlyResponses(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "以下の通りです。\n詳しく説明します。\n最新のものはgo1.26.5で、.RELEASE=MONTH_DAY。"}},
		{{DeltaText: "参考情報です。"}},
		{{DeltaText: "**追加情報です。**"}},
		{{DeltaText: "詳しく説明します。"}},
	}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))
	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model: "fake/m",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Goの特徴を教えて"},
			{Role: llm.RoleAssistant, Content: "静的型付けです。"},
			{Role: llm.RoleUser, Content: "詳しく。"},
		},
		MaxToolHops: 0,
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	var final *llm.Message
	for event := range out {
		if event.Kind == agent.EventFinal {
			final = event.Final
		}
	}
	if final == nil {
		t.Fatal("EventFinal not emitted")
	}
	if len(prov.requests) != 4 {
		t.Fatalf("requests=%d, want initial request and three retries", len(prov.requests))
	}
	temperature := func(value float64) *float64 { return &value }
	wantTemperatures := []*float64{nil, temperature(0.3), temperature(0.6), temperature(0.9)}
	for i, request := range prov.requests {
		if !reflect.DeepEqual(request.Temperature, wantTemperatures[i]) {
			t.Errorf("request[%d].Temperature=%v, want %v", i, request.Temperature, wantTemperatures[i])
		}
	}
	for _, introduction := range []string{"以下の通りです。", "詳しく説明します。", "参考情報です。", "追加情報です。", ".RELEASE=MONTH_DAY"} {
		if strings.Trim(final.Content, "* \n") == introduction {
			t.Errorf("introduction-only content %q was accepted: %q", introduction, final.Content)
		}
	}
	if !strings.Contains(final.Content, "追加情報を生成できませんでした") {
		t.Errorf("fallback=%q, want explicit shortage", final.Content)
	}
}

func TestRun_NewTopicQuestionWithDetailWordIsNotTreatedAsFollowUp(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{DeltaText: "Rustの所有権は値の解放責任を単一の変数に持たせる仕組みです。借用は参照による一時的なアクセスで、ライフタイムは参照の有効期間を表します。"}},
	}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))
	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model: "fake/m",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "Goの最新版をWebで検索して教えて"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "webf00001", Name: "web_fetch"}}},
			{Role: llm.RoleTool, Name: "web_fetch", ToolCallID: "webf00001", Content: "[UNTRUSTED INPUT: tool=web_fetch]\n2026年7月7日にgo1.26.5がリリースされました。\n[END UNTRUSTED]"},
			{Role: llm.RoleAssistant, Content: "最新の安定版リリースは go1.26.5 です。"},
			{Role: llm.RoleUser, Content: "Rustの所有権システムとは何ですか。借用やライフタイムとの関係も含めて詳しく説明してください。"},
		},
		MaxToolHops: 0,
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	var deltas strings.Builder
	var final *llm.Message
	for event := range out {
		if event.Kind == agent.EventDelta {
			deltas.WriteString(event.Delta)
		}
		if event.Kind == agent.EventFinal {
			final = event.Final
		}
	}
	if len(prov.requests) != 1 {
		t.Fatalf("requests=%d, want 1 (no expansion retries for a new topic question)", len(prov.requests))
	}
	if final == nil {
		t.Fatal("EventFinal not emitted")
	}
	if !strings.Contains(final.Content, "所有権") || !strings.Contains(deltas.String(), "所有権") {
		t.Errorf("answer to the new question was lost: final=%q deltas=%q", final.Content, deltas.String())
	}
	if strings.Contains(final.Content, "追加情報") || strings.Contains(final.Content, "go1.26.5") {
		t.Errorf("final was hijacked by expansion mode: %q", final.Content)
	}
}

func TestRun_InitialDetailedQuestionDoesNotAddFollowUpConstraint(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{{{DeltaText: "説明"}}}}
	svc := agent.New(fakeReg{p: prov}, tool.NewRegistry(nil, nil))

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:        "fake/m",
		SystemPrompt: "日本語で答える",
		Messages:     []llm.Message{{Role: llm.RoleUser, Content: "Goを詳しく説明して"}},
		MaxToolHops:  1,
	}, out)
	close(out)
	for range out {
	}
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	if got := prov.requests[0].Messages[0].Content; got != "日本語で答える" {
		t.Errorf("system prompt=%q, want unchanged", got)
	}
}

func TestRun_FinalIncludesTurnMessages(t *testing.T) {
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"value":"x"}`)}}},
		{{DeltaText: "done"}},
	}}
	tools := tool.NewRegistry([]tool.Tool{echoTool{}}, []string{"echo"})
	svc := agent.New(fakeReg{p: prov}, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "echo"}},
		MaxToolHops: 2,
	}, out)
	close(out)
	if err != nil {
		t.Fatalf("run err=%v", err)
	}
	var final *agent.Event
	for ev := range out {
		if ev.Kind == agent.EventFinal {
			evCopy := ev
			final = &evCopy
		}
	}
	if final == nil {
		t.Fatal("EventFinal not emitted")
	}
	if len(final.TurnMessages) != 3 {
		t.Fatalf("TurnMessages=%d, want 3: %+v", len(final.TurnMessages), final.TurnMessages)
	}
	if final.TurnMessages[0].Role != llm.RoleAssistant || len(final.TurnMessages[0].ToolCalls) != 1 {
		t.Errorf("first turn message=%+v, want assistant tool call", final.TurnMessages[0])
	}
	if final.TurnMessages[1].Role != llm.RoleTool || final.TurnMessages[1].ToolCallID != "c1" {
		t.Errorf("second turn message=%+v, want tool result", final.TurnMessages[1])
	}
	if final.TurnMessages[2].Role != llm.RoleAssistant || final.TurnMessages[2].Content != "done" {
		t.Errorf("third turn message=%+v, want final assistant", final.TurnMessages[2])
	}
}
