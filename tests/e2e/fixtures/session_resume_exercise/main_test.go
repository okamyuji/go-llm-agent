package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func TestHasEarlierTurn_SingleMessageIsNotEarlier(t *testing.T) {
	m := llm.Message{Role: llm.RoleUser, Content: session1Prompt}
	if hasEarlierTurn([]llm.Message{m}, m) {
		t.Fatal("単独メッセージは以前のターンとみなさない")
	}
}

func TestHasEarlierTurn_EarlierMessageBeforeLatestIsEarlier(t *testing.T) {
	earlier := llm.Message{Role: llm.RoleUser, Content: session1Prompt}
	all := []llm.Message{earlier, {Role: llm.RoleAssistant, Content: "session1 answer"}, {Role: llm.RoleUser, Content: "hello-session2"}}
	if !hasEarlierTurn(all, earlier) {
		t.Fatal("先頭のメッセージは以前のターンとみなすべき")
	}
}

func TestHasEarlierTurn_EmptySliceIsFalse(t *testing.T) {
	if hasEarlierTurn(nil, llm.Message{}) {
		t.Fatal("空スライスは false")
	}
}

func TestStubProvider_Stream_NoEarlierTurnReturnsDefaultAnswer(t *testing.T) {
	stream, err := stubProvider{}.Stream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: llm.RoleUser, Content: session1Prompt}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	ev, ok := stream.Recv()
	if !ok || ev.DeltaText != "session1 answer" {
		t.Fatalf("ev=%+v ok=%v", ev, ok)
	}
}

func TestStubProvider_Stream_EarlierTurnReturnsResumedOK(t *testing.T) {
	stream, err := stubProvider{}.Stream(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: session1Prompt},
			{Role: llm.RoleAssistant, Content: "session1 answer"},
			{Role: llm.RoleUser, Content: "hello-session2"},
		},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	ev, ok := stream.Recv()
	if !ok || ev.DeltaText != "resumed_ok" {
		t.Fatalf("ev=%+v ok=%v", ev, ok)
	}
}

func TestStubProvider_Chat_ReturnsError(t *testing.T) {
	if _, err := (stubProvider{}).Chat(context.Background(), llm.ChatRequest{}); err == nil {
		t.Fatal("Chat は使わない前提のためエラーを返すこと")
	}
}

func TestCountJSONLFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.jsonl", "b.jsonl", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write err=%v", err)
		}
	}
	if got := countJSONLFiles(dir); got != 2 {
		t.Fatalf("got=%d, want 2", got)
	}
}

func TestCountJSONLFiles_MissingDirReturnsZero(t *testing.T) {
	if got := countJSONLFiles(filepath.Join(t.TempDir(), "missing")); got != 0 {
		t.Fatalf("got=%d, want 0", got)
	}
}

func TestRun_AllChecksPass(t *testing.T) {
	base := t.TempDir()
	var out bytes.Buffer
	if err := run(context.Background(), base, &out); err != nil {
		t.Fatalf("run err=%v", err)
	}
	for _, key := range []string{
		"session1_file_created=true",
		"chat_dir_fallback_ok=true",
		"resume_flag_path_ok=true",
		"session2_sees_session1=true",
		"resume_empty_dir_ok=true",
		"broken_line_skipped=true",
	} {
		if !strings.Contains(out.String(), key) {
			t.Fatalf("missing %q in output: %s", key, out.String())
		}
	}
}
