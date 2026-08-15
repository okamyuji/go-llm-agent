package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/okamyuji/go-llm-agent/internal/agent"
)

func TestBuildFixtureContent_MatchesWrappedTarget(t *testing.T) {
	content, wrappedChars := buildFixtureContent()
	if wrappedChars != wrappedTarget {
		t.Fatalf("wrappedChars = %d, want %d", wrappedChars, wrappedTarget)
	}
	if !strings.HasSuffix(content, "EXIT_CODE=1") {
		t.Fatalf("content does not end with EXIT_CODE=1: %q", content[max(0, len(content)-20):])
	}
}

func TestRunOnce_EnabledLimitTruncatesHistory(t *testing.T) {
	content, _ := buildFixtureContent()
	historyContent, fullContent, err := runOnce(8000, content)
	if err != nil {
		t.Fatalf("runOnce err=%v", err)
	}
	if !strings.Contains(historyContent, "…[truncated:") {
		t.Fatalf("historyContent not truncated: %q", historyContent[:60])
	}
	if utf8.RuneCountInString(fullContent) != wrappedTarget {
		t.Fatalf("fullContent chars = %d, want %d", utf8.RuneCountInString(fullContent), wrappedTarget)
	}
}

func TestRunOnce_DisabledLimitKeepsFullHistory(t *testing.T) {
	content, _ := buildFixtureContent()
	historyContent, _, err := runOnce(-1, content)
	if err != nil {
		t.Fatalf("runOnce err=%v", err)
	}
	if strings.Contains(historyContent, "…[truncated:") {
		t.Fatalf("historyContent should not be truncated when limit disabled: %q", historyContent[:60])
	}
	if utf8.RuneCountInString(historyContent) != wrappedTarget {
		t.Fatalf("historyContent chars = %d, want %d (unchanged)", utf8.RuneCountInString(historyContent), wrappedTarget)
	}
}

func TestExtractResults_NoFinalEventReturnsError(t *testing.T) {
	ch := make(chan agent.Event)
	close(ch)
	_, _, err := extractResults(ch)
	if err == nil {
		t.Fatal("want error when no EventFinal was emitted")
	}
}

func TestToolMessageContent_NoToolMessageReturnsEmpty(t *testing.T) {
	got := toolMessageContent(nil)
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}
