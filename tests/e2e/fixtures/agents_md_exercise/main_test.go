package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func TestComposeSystemPrompt_ContentInsideMarkers(t *testing.T) {
	got := composeSystemPrompt("base", "content-X")
	if !strings.Contains(got, "content-X") || !strings.Contains(got, "[UNTRUSTED PROJECT FILE: AGENTS.md]") {
		t.Fatalf("got=%q", got)
	}
	if !strings.HasPrefix(got, "base") {
		t.Fatalf("base should stay at the front: %q", got)
	}
}

func TestSeenProvider_Stream_MarkerPresentReturnsTrue(t *testing.T) {
	stream, err := seenProvider{}.Stream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleSystem, Content: "base " + agentsMDMarker + " tail"}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	ev, ok := stream.Recv()
	if !ok || ev.DeltaText != "agents_md_seen=true" {
		t.Fatalf("ev=%+v ok=%v", ev, ok)
	}
	if _, ok2 := stream.Recv(); ok2 {
		t.Fatal("stream should have exactly one event")
	}
}

func TestSeenProvider_Stream_MarkerAbsentReturnsFalse(t *testing.T) {
	stream, err := seenProvider{}.Stream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleSystem, Content: "base only"}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	ev, ok := stream.Recv()
	if !ok || ev.DeltaText != "agents_md_seen=false" {
		t.Fatalf("ev=%+v ok=%v", ev, ok)
	}
}

func TestSeenProvider_Stream_NoMessagesReturnsFalse(t *testing.T) {
	stream, err := seenProvider{}.Stream(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	ev, ok := stream.Recv()
	if !ok || ev.DeltaText != "agents_md_seen=false" {
		t.Fatalf("ev=%+v ok=%v", ev, ok)
	}
}

func TestSeenProvider_Chat_ReturnsError(t *testing.T) {
	if _, err := (seenProvider{}).Chat(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("Chat は使わない前提のためエラーを返すこと")
	}
}

func TestRunOnce_ReturnsProviderResponse(t *testing.T) {
	out := runOnce(context.Background(), "base "+agentsMDMarker)
	if !strings.Contains(out, "agents_md_seen=true") {
		t.Fatalf("out=%q", out)
	}
}

func TestCheckAgentsMDPresent_WritesTrueWhenMarkerPropagates(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	checkAgentsMDPresent(context.Background(), dir, &buf)
	if !strings.Contains(buf.String(), "agents_md_prompt_applied=true") {
		t.Fatalf("buf=%q", buf.String())
	}
	if _, err := os.Stat(dir + "/AGENTS.md"); err != nil {
		t.Fatalf("AGENTS.md should have been written: %v", err)
	}
}

func TestCheckAgentsMDAbsent_WritesTrueWhenNoFile(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	checkAgentsMDAbsent(context.Background(), dir, &buf)
	if !strings.Contains(buf.String(), "agents_md_absent_ok=true") {
		t.Fatalf("buf=%q", buf.String())
	}
}
