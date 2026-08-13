package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func TestRun_ToolResultTruncatedInHistory(t *testing.T) {
	longContent := strings.Repeat("x", 200)
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "big", Arguments: json.RawMessage(`{}`)}}},
		{{DeltaText: "done"}},
	}}
	bigTool := namedTool{name: "big", content: longContent}
	tools := tool.NewRegistry([]tool.Tool{bigTool}, []string{"big"})
	svc := agent.New(fakeReg{p: prov}, tools, agent.WithToolResultLimit(50))

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "run big"}},
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
	var toolMsg *llm.Message
	for i := range final.TurnMessages {
		if final.TurnMessages[i].Role == llm.RoleTool {
			toolMsg = &final.TurnMessages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("no RoleTool message found in TurnMessages")
	}
	if !strings.Contains(toolMsg.Content, "…[truncated:") {
		t.Fatalf("history content not truncated: %q", toolMsg.Content)
	}
	if strings.Contains(toolMsg.Content, longContent) {
		t.Fatalf("history content still contains full untruncated payload: %q", toolMsg.Content)
	}
}

func TestRun_ToolResultNotTruncatedWhenLimitDisabled(t *testing.T) {
	longContent := strings.Repeat("y", 200)
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "big", Arguments: json.RawMessage(`{}`)}}},
		{{DeltaText: "done"}},
	}}
	bigTool := namedTool{name: "big", content: longContent}
	tools := tool.NewRegistry([]tool.Tool{bigTool}, []string{"big"})
	// WithToolResultLimit を注入しない (既定 0 = 無効)
	svc := agent.New(fakeReg{p: prov}, tools)

	out := make(chan agent.Event, 16)
	err := svc.Run(context.Background(), agent.Input{
		Model:       "fake/m",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "run big"}},
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
	var toolMsg *llm.Message
	for i := range final.TurnMessages {
		if final.TurnMessages[i].Role == llm.RoleTool {
			toolMsg = &final.TurnMessages[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("no RoleTool message found in TurnMessages")
	}
	if !strings.Contains(toolMsg.Content, longContent) {
		t.Fatalf("history content should contain full untruncated payload: %q", toolMsg.Content)
	}
}

func TestRun_EventToolResultShowsFullContentEvenWhenTruncated(t *testing.T) {
	longContent := strings.Repeat("z", 200)
	prov := &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "big", Arguments: json.RawMessage(`{}`)}}},
		{{DeltaText: "done"}},
	}}
	bigTool := namedTool{name: "big", content: longContent}
	tools := tool.NewRegistry([]tool.Tool{bigTool}, []string{"big"})
	svc := agent.New(fakeReg{p: prov}, tools, agent.WithToolResultLimit(50))

	out := make(chan agent.Event, 16)
	done := make(chan struct{})
	var toolResultContent string
	go func() {
		_ = svc.Run(context.Background(), agent.Input{
			Model:       "fake/m",
			Messages:    []llm.Message{{Role: llm.RoleUser, Content: "run big"}},
			MaxToolHops: 2,
		}, out)
		close(out)
		close(done)
	}()
	for ev := range out {
		if ev.Kind == agent.EventToolResult && ev.ToolResult != nil {
			toolResultContent = ev.ToolResult.Content
		}
	}
	<-done
	if !strings.Contains(toolResultContent, longContent) {
		t.Fatalf("EventToolResult content should be full (untruncated): %q", toolResultContent)
	}
}
