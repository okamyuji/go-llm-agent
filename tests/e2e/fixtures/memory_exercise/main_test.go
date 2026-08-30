package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/memory"
)

func newStore(t *testing.T) *memory.Store {
	t.Helper()
	st, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

func TestComposeMemoryPrompt_IndexInsideMarkers(t *testing.T) {
	got := composeMemoryPrompt("base", "- fact")
	if !strings.Contains(got, "- fact") || !strings.Contains(got, "[AGENT MEMORY]") {
		t.Fatalf("got=%q", got)
	}
	if !strings.HasPrefix(got, "base") {
		t.Fatalf("base should stay at the front: %q", got)
	}
}

func TestSeenProvider_Stream_FactPresentReturnsTrue(t *testing.T) {
	stream, err := seenProvider{}.Stream(t.Context(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleSystem, Content: "base " + memoryFact + " tail"}},
	})
	if err != nil {
		t.Fatalf("Stream err=%v", err)
	}
	ev, ok := stream.Recv()
	if !ok || ev.DeltaText != "memory_seen=true" {
		t.Fatalf("ev=%+v ok=%v", ev, ok)
	}
}

func TestSeenProvider_Stream_FactAbsentReturnsFalse(t *testing.T) {
	stream, err := seenProvider{}.Stream(t.Context(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleSystem, Content: "base"}},
	})
	if err != nil {
		t.Fatalf("Stream err=%v", err)
	}
	ev, _ := stream.Recv()
	if ev.DeltaText != "memory_seen=false" {
		t.Fatalf("ev=%+v", ev)
	}
	if _, ok := stream.Recv(); ok {
		t.Fatalf("2 回目の Recv が ok を返した")
	}
}

func TestSeenProvider_Chat_ReturnsError(t *testing.T) {
	if _, err := (seenProvider{}).Chat(t.Context(), llm.ChatRequest{}); err == nil {
		t.Fatalf("Chat は使わない契約のためエラーを返すべき")
	}
}

func TestCheckHashSaves_WritesTrue(t *testing.T) {
	st := newStore(t)
	var w bytes.Buffer
	checkHashSaves(t.Context(), st, &w)
	if !strings.Contains(w.String(), "memory_hash_saved=true") {
		t.Fatalf("out=%q", w.String())
	}
}

func TestCheckIndexInjected_TrueAfterIndexExists(t *testing.T) {
	st := newStore(t)
	if err := st.Write(memory.IndexFileName, "- "+memoryFact+"\n", false); err != nil {
		t.Fatalf("prep: %v", err)
	}
	var w bytes.Buffer
	checkIndexInjected(t.Context(), st, &w)
	if !strings.Contains(w.String(), "memory_index_injected=true") {
		t.Fatalf("out=%q", w.String())
	}
}

func TestCheckIndexInjected_FalseWithoutIndex(t *testing.T) {
	st := newStore(t)
	var w bytes.Buffer
	checkIndexInjected(t.Context(), st, &w)
	if !strings.Contains(w.String(), "memory_index_injected=false") {
		t.Fatalf("out=%q", w.String())
	}
}

func TestCheckToolRoundTrip_WritesTrue(t *testing.T) {
	st := newStore(t)
	var w bytes.Buffer
	checkToolRoundTrip(t.Context(), st, &w)
	if !strings.Contains(w.String(), "memory_tool_roundtrip=true") {
		t.Fatalf("out=%q", w.String())
	}
}
