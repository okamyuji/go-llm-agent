package audit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func readAllEvents(t *testing.T, dir string) []Event {
	t.Helper()
	var out []Event
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		recs, _ := readFrom(p, 0, 100000)
		for _, r := range recs {
			var e Event
			_ = json.Unmarshal(r.Line, &e)
			out = append(out, e)
		}
		return nil
	})
	return out
}

func TestNilEmitterIsNoop(t *testing.T) {
	var e *Emitter
	e.ToolCall(context.Background(), llm.ToolCall{ID: "c", Name: "x"})
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEmitterIsLazy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wal")
	e := NewEmitter(Options{WALDir: dir, IggyURL: "http://127.0.0.1:1", PAT: "p"})
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("NewEmitter must not create files")
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("Shutdown on uninitialized emitter must not create files")
	}
}

func TestEmitterWritesEventsWithSessionAndFallback(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p", Redactor: upperRedactor{}})
	ctx := WithSessionID(context.Background(), "sess1")
	e.ToolCall(ctx, llm.ToolCall{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"cmd":"echo SECRET"}`)})
	e.ToolResult(ctx, "c1", "shell", "SECRET out", false, 5*time.Millisecond)
	e.ToolCall(context.Background(), llm.ToolCall{ID: "c2", Name: "shell", Arguments: json.RawMessage(`{}`)}) // session 無し
	e.Usage(ctx, "llamacpp", "m", llm.Usage{InputTokens: 1, OutputTokens: 2})
	_ = e.Shutdown(context.Background())
	evs := readAllEvents(t, dir)
	if len(evs) != 4 {
		t.Fatalf("events=%d", len(evs))
	}
	var seenFallback, seenRedactedArgs bool
	for _, ev := range evs {
		if ev.SessionID == "run-"+e.RunID() {
			seenFallback = true
		}
		if ev.Kind == KindToolCall && ev.CallID == "c1" {
			var p ToolCallPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if string(p.Arguments) == `{"cmd":"echo [R]"}` {
				seenRedactedArgs = true
			}
		}
		if ev.Kind == KindToolResult {
			var p ToolResultPayload
			_ = json.Unmarshal(ev.Payload, &p)
			if p.DurationMS != 5 || p.Content != "SECRET out" {
				t.Errorf("tool_result payload=%s", ev.Payload) // content は呼び手が redact 済みの値を渡す前提で素通し
			}
		}
	}
	if !seenFallback || !seenRedactedArgs {
		t.Fatalf("fallback=%v redactedArgs=%v", seenFallback, seenRedactedArgs)
	}
	if len(fi.received) != 4 {
		t.Fatalf("iggy received=%d", len(fi.received))
	}
}

func TestEmitterInitOnce(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p"})
	ctx := WithSessionID(context.Background(), "s")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.Usage(ctx, "p", "m", llm.Usage{InputTokens: 1}) }()
	}
	wg.Wait()
	_ = e.Shutdown(context.Background())
	if fi.logins.Load() != 1 {
		t.Fatalf("logins=%d", fi.logins.Load())
	}
	locks, _ := filepath.Glob(filepath.Join(dir, "*.lock"))
	if len(locks) != 1 {
		t.Fatalf("locks=%d", len(locks))
	}
}

func TestEmitterLLMRequestRedactsMessagesAndToolCallArgs(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p", Redactor: upperRedactor{}})
	ctx := WithSessionID(context.Background(), "s")
	e.LLMRequest(ctx, "llamacpp", "m", llm.ChatRequest{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: "SECRET"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c", Name: "shell", Arguments: json.RawMessage(`{"a":"SECRET"}`)}}},
	}})
	_ = e.Shutdown(context.Background())
	evs := readAllEvents(t, dir)
	var p LLMRequestPayload
	_ = json.Unmarshal(evs[0].Payload, &p)
	if p.Messages[0].Content != "[R]" || string(p.Messages[1].ToolCalls[0].Arguments) != `{"a":"[R]"}` {
		t.Fatalf("payload=%s", evs[0].Payload)
	}
}

// TestEmitterShutdownWaitsForSenderDone は run() を Emitter 経由で起動し、
// Shutdown が sender.done を待って戻ることを確認する（Task 5 が未検証のまま残した経路）
func TestEmitterShutdownWaitsForSenderDone(t *testing.T) {
	dir := t.TempDir()
	fi := newFakeIggy(t)
	e := NewEmitter(Options{WALDir: dir, IggyURL: fi.srv.URL, PAT: "p"})
	ctx := WithSessionID(context.Background(), "s")
	e.Usage(ctx, "p", "m", llm.Usage{InputTokens: 1})
	e.Usage(ctx, "p", "m", llm.Usage{InputTokens: 2})
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if len(fi.received) != 2 {
		t.Fatalf("received=%d, want 2 (sender.run must have drained before Shutdown returned)", len(fi.received))
	}
}
