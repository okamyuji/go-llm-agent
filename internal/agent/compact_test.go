package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// summarizer Chat だけを実装する要約器のテストダブル
type summarizer struct {
	summary  string
	err      error
	calls    int
	requests []llm.ChatRequest
}

func (s *summarizer) Name() string { return "summarizer" }

func (s *summarizer) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	s.calls++
	s.requests = append(s.requests, req)
	if s.err != nil {
		return nil, s.err
	}
	return &llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: s.summary}}, nil
}

func (s *summarizer) Stream(_ context.Context, _ llm.ChatRequest) (llm.ChatStream, error) {
	return nil, errors.New("stream は使わない")
}

// conversation system 1 通 + turns 件の user/assistant ターンを組み立てる
func conversation(turns int) []llm.Message {
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "sys"}}
	for i := range turns {
		msgs = append(msgs,
			llm.Message{Role: llm.RoleUser, Content: "q" + string(rune('0'+i))},
			llm.Message{Role: llm.RoleAssistant, Content: "a" + string(rune('0'+i))},
		)
	}
	return msgs
}

func TestCompactMessages_ReplacesOldTurnsWithSummary(t *testing.T) {
	prov := &summarizer{summary: "要約本文"}
	msgs := conversation(6)
	got, err := agent.CompactMessages(context.Background(), prov, "m", msgs, agent.CompactOptions{KeepRecentTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	// system 1 + 結合した user 1 + 残り (a4, q5, a5) = 5
	if len(got) != 5 {
		t.Fatalf("5 件期待 got %d (%+v)", len(got), got)
	}
	if got[0].Role != llm.RoleSystem {
		t.Fatalf("system を保持する期待 got %v", got[0].Role)
	}
	head := got[1]
	if head.Role != llm.RoleUser {
		t.Fatalf("結合先は user 期待 got %v", head.Role)
	}
	if !strings.HasPrefix(head.Content, "[過去の会話の要約]") {
		t.Fatalf("要約見出しで始まる期待 got %q", head.Content)
	}
	if !strings.Contains(head.Content, "要約本文") || !strings.Contains(head.Content, "[ここから現在の会話]") || !strings.HasSuffix(head.Content, "q4") {
		t.Fatalf("要約と元の発話が結合される期待 got %q", head.Content)
	}
	if prov.calls != 1 {
		t.Fatalf("要約呼び出し 1 回期待 got %d", prov.calls)
	}
	if !strings.Contains(prov.requests[0].Messages[1].Content, "USER: q0") {
		t.Fatalf("会話ログを渡す期待 got %q", prov.requests[0].Messages[1].Content)
	}
}

func TestCompactMessages_NoConsecutiveUserRoles(t *testing.T) {
	for _, keep := range []int{0, 1, 2, 4} {
		prov := &summarizer{summary: "s"}
		got, err := agent.CompactMessages(context.Background(), prov, "m", conversation(6), agent.CompactOptions{KeepRecentTurns: keep})
		if err != nil {
			t.Fatalf("keep=%d: %v", keep, err)
		}
		for i := 1; i < len(got); i++ {
			if got[i].Role == llm.RoleUser && got[i-1].Role == llm.RoleUser {
				t.Fatalf("keep=%d: user が連続している index=%d", keep, i)
			}
		}
	}
}

func TestCompactMessages_NoKeptTurns_SummaryIsStandaloneUser(t *testing.T) {
	prov := &summarizer{summary: "s"}
	got, err := agent.CompactMessages(context.Background(), prov, "m", conversation(3), agent.CompactOptions{KeepRecentTurns: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("system + 要約 1 通期待 got %d", len(got))
	}
	last := got[len(got)-1]
	if last.Role != llm.RoleUser || !strings.HasPrefix(last.Content, "[過去の会話の要約]") {
		t.Fatalf("末尾は独立した user の要約期待 got %+v", last)
	}
}

func TestCompactMessages_KeepRecentTurnsBoundary_NoOp(t *testing.T) {
	for _, keep := range []int{3, 4} {
		prov := &summarizer{summary: "s"}
		msgs := conversation(3)
		got, err := agent.CompactMessages(context.Background(), prov, "m", msgs, agent.CompactOptions{KeepRecentTurns: keep})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(msgs) {
			t.Fatalf("keep=%d: no-op 期待 got %d", keep, len(got))
		}
		if prov.calls != 0 {
			t.Fatalf("keep=%d: 要約を呼ばない期待 got %d", keep, prov.calls)
		}
	}
}

func TestCompactMessages_OnlySystemMessages_NoOp(t *testing.T) {
	prov := &summarizer{summary: "s"}
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "sys"}}
	got, err := agent.CompactMessages(context.Background(), prov, "m", msgs, agent.CompactOptions{KeepRecentTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || prov.calls != 0 {
		t.Fatalf("no-op 期待 got %d 件 calls=%d", len(got), prov.calls)
	}
}

func TestCompactMessages_ZeroKeepRecentTurns_SummarizesEverythingExceptSystem(t *testing.T) {
	prov := &summarizer{summary: "s"}
	msgs := conversation(2)
	got, err := agent.CompactMessages(context.Background(), prov, "m", msgs, agent.CompactOptions{KeepRecentTurns: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("system 以外は全て要約対象期待 got %d", len(got))
	}
	transcript := prov.requests[0].Messages[1].Content
	for _, want := range []string{"USER: q0", "ASSISTANT: a0", "USER: q1", "ASSISTANT: a1"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("%q を含む期待 got %q", want, transcript)
		}
	}
	if strings.Contains(transcript, "sys") {
		t.Fatalf("system は要約対象外期待 got %q", transcript)
	}
}

func TestCompactMessages_NegativeKeepRecentTurns_TreatedAsZero(t *testing.T) {
	provNeg := &summarizer{summary: "s"}
	gotNeg, err := agent.CompactMessages(context.Background(), provNeg, "m", conversation(3), agent.CompactOptions{KeepRecentTurns: -1})
	if err != nil {
		t.Fatal(err)
	}
	provZero := &summarizer{summary: "s"}
	gotZero, err := agent.CompactMessages(context.Background(), provZero, "m", conversation(3), agent.CompactOptions{KeepRecentTurns: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotNeg) != len(gotZero) || gotNeg[len(gotNeg)-1].Content != gotZero[len(gotZero)-1].Content {
		t.Fatalf("-1 は 0 と同じ挙動期待 got %+v / %+v", gotNeg, gotZero)
	}
}

func TestCompactMessages_ChatError_ReturnsError(t *testing.T) {
	prov := &summarizer{err: errors.New("boom")}
	got, err := agent.CompactMessages(context.Background(), prov, "m", conversation(3), agent.CompactOptions{KeepRecentTurns: 1})
	if err == nil {
		t.Fatal("エラー期待")
	}
	if got != nil {
		t.Fatalf("エラー時は nil 期待 got %+v", got)
	}
}

func TestCompactMessages_EmptySummary_ReturnsError(t *testing.T) {
	prov := &summarizer{summary: "   "}
	got, err := agent.CompactMessages(context.Background(), prov, "m", conversation(3), agent.CompactOptions{KeepRecentTurns: 1})
	if err == nil || got != nil {
		t.Fatalf("空要約はエラー期待 got %+v err=%v", got, err)
	}
}

func TestCompactMessages_ToolMessagesInTranscript(t *testing.T) {
	prov := &summarizer{summary: "s"}
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "sys"},
		{Role: llm.RoleUser, Content: "q0"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "shell"}}},
		{Role: llm.RoleTool, Name: "shell", ToolCallID: "c1", Content: "output"},
		{Role: llm.RoleAssistant, Content: "a0"},
		{Role: llm.RoleUser, Content: "q1"},
		{Role: llm.RoleAssistant, Content: "a1"},
	}
	if _, err := agent.CompactMessages(context.Background(), prov, "m", msgs, agent.CompactOptions{KeepRecentTurns: 1}); err != nil {
		t.Fatal(err)
	}
	transcript := prov.requests[0].Messages[1].Content
	for _, want := range []string{"[tool_call shell]", "TOOL[shell]: output"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("%q を含む期待 got %q", want, transcript)
		}
	}
}
