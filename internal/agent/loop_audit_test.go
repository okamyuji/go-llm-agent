package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/audit"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func readAuditEvents(t *testing.T, dir string) []audit.Event {
	t.Helper()
	var out []audit.Event
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, line := range splitLines(b) {
			var e audit.Event
			if json.Unmarshal(line, &e) == nil {
				out = append(out, e)
			}
		}
		return nil
	})
	return out
}

// splitLines 改行区切り。末尾の空要素は除く
func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			if i > start {
				out = append(out, b[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func countKinds(evs []audit.Event) map[audit.Kind]int {
	m := map[audit.Kind]int{}
	for _, e := range evs {
		m[e.Kind]++
	}
	return m
}

// newAuditEmitterForTest Iggy へは繋がらない URL を与える。WAL への追記だけを検証する
func newAuditEmitterForTest(t *testing.T) (*audit.Emitter, string) {
	t.Helper()
	dir := t.TempDir()
	e := audit.NewEmitter(audit.Options{WALDir: dir, IggyURL: "http://127.0.0.1:1", PAT: "p"})
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
	return e, dir
}

// newScriptedProviderForAudit 1 ホップ目で shell ツールを呼び、2 ホップ目で最終回答を返す偽 Provider
func newScriptedProviderForAudit(t *testing.T) *fakeProvider {
	t.Helper()
	return &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"cmd":"echo hi"}`)}}},
		{{DeltaText: "done"}},
	}}
}

// newScriptedProviderCallingTool 1 ホップ目で指定名のツールを呼ぶ偽 Provider
func newScriptedProviderCallingTool(t *testing.T, toolName string) *fakeProvider {
	t.Helper()
	return &fakeProvider{streams: [][]llm.StreamEvent{
		{{ToolCall: &llm.ToolCall{ID: "c1", Name: toolName, Arguments: json.RawMessage(`{}`)}}},
		{{DeltaText: "done"}},
	}}
}

// newToolRegistryForAudit 成功する偽ツール "shell" を 1 つ登録する
func newToolRegistryForAudit(t *testing.T) tool.Registry {
	t.Helper()
	return tool.NewRegistry([]tool.Tool{
		namedTool{name: "shell", content: "hi"},
	}, []string{"shell"})
}

func newRegistryFor(p llm.Provider) llm.Registry {
	return fakeReg{p: p}
}

func TestRunRecordsToolCallAndResultWithSessionID(t *testing.T) {
	e, dir := newAuditEmitterForTest(t)
	prov := newScriptedProviderForAudit(t)
	tools := newToolRegistryForAudit(t)
	svc := agent.New(newRegistryFor(prov), tools, agent.WithEmitter(e))
	out := make(chan agent.Event, 100)
	err := svc.Run(context.Background(), agent.Input{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "go"}}, SessionID: "sess-1", MaxToolHops: 3, EnabledTools: []string{"shell"}}, out)
	close(out)
	if err != nil {
		t.Fatal(err)
	}
	_ = e.Shutdown(context.Background())
	evs := readAuditEvents(t, dir)
	k := countKinds(evs)
	if k[audit.KindToolCall] != 1 || k[audit.KindToolResult] != 1 {
		t.Fatalf("kinds=%v", k)
	}
	var sawDuration bool
	for _, ev := range evs {
		if ev.SessionID != "sess-1" {
			t.Fatalf("session_id=%q kind=%s", ev.SessionID, ev.Kind)
		}
		if ev.Kind == audit.KindToolResult {
			var p audit.ToolResultPayload
			if uerr := json.Unmarshal(ev.Payload, &p); uerr != nil {
				t.Fatal(uerr)
			}
			if p.IsError {
				t.Fatalf("成功結果は is_error=false 期待 got %+v", p)
			}
			if p.DurationMS >= 0 {
				sawDuration = true
			}
		}
	}
	if !sawDuration {
		t.Fatal("tool_result に duration_ms が含まれる期待")
	}
}

func TestDeniedToolCallStillGetsToolResult(t *testing.T) {
	e, dir := newAuditEmitterForTest(t)
	prov := newScriptedProviderForAudit(t)
	tools := newToolRegistryForAudit(t)
	svc := agent.New(newRegistryFor(prov), tools, agent.WithEmitter(e),
		agent.WithApprovalDecider(agent.AutoDecider{Allow: false, Reason: "nope"}, []string{"shell"}, 0))
	out := make(chan agent.Event, 100)
	_ = svc.Run(context.Background(), agent.Input{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "go"}}, SessionID: "s", MaxToolHops: 3, EnabledTools: []string{"shell"}}, out)
	close(out)
	_ = e.Shutdown(context.Background())
	evs := readAuditEvents(t, dir)
	k := countKinds(evs)
	if k[audit.KindToolCall] != 1 || k[audit.KindToolResult] != 1 {
		t.Fatalf("kinds=%v", k)
	}
	for _, ev := range evs {
		if ev.Kind == audit.KindToolResult {
			var p audit.ToolResultPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if !p.IsError {
				t.Fatal("denied call must produce is_error=true")
			}
		}
	}
}

func TestUnknownToolProducesToolResultAtCaller(t *testing.T) {
	e, dir := newAuditEmitterForTest(t)
	prov := newScriptedProviderCallingTool(t, "no_such_tool")
	tools := newToolRegistryForAudit(t)
	svc := agent.New(newRegistryFor(prov), tools, agent.WithEmitter(e))
	out := make(chan agent.Event, 100)
	_ = svc.Run(context.Background(), agent.Input{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "go"}}, SessionID: "s", MaxToolHops: 3, EnabledTools: []string{"shell"}}, out)
	close(out)
	_ = e.Shutdown(context.Background())
	k := countKinds(readAuditEvents(t, dir))
	if k[audit.KindToolCall] != 1 || k[audit.KindToolResult] != 1 {
		t.Fatalf("kinds=%v", k)
	}
}

func TestParallelOutcomeCarriesDuration(t *testing.T) {
	var o agent.ParallelOutcome
	if o.Duration != 0 {
		t.Fatalf("ゼロ値期待 got %v", o.Duration)
	}
}
