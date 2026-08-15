package agent

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateToolResult_UnderLimit_ReturnsUnchanged(t *testing.T) {
	content := "short content"
	got := TruncateToolResult(content, 1000)
	if got != content {
		t.Fatalf("got %q, want unchanged %q", got, content)
	}
}

func TestTruncateToolResult_ExactlyAtLimit_ReturnsUnchanged(t *testing.T) {
	content := strings.Repeat("x", 100)
	got := TruncateToolResult(content, 100)
	if got != content {
		t.Fatalf("got %q, want unchanged content of len 100", got)
	}
}

func TestTruncateToolResult_OneOverLimit_Truncates(t *testing.T) {
	content := strings.Repeat("x", 101)
	got := TruncateToolResult(content, 100)
	if !strings.Contains(got, "…[truncated: 1 chars omitted]…") {
		t.Fatalf("got %q, want marker with 1 chars omitted", got)
	}
}

func TestTruncateToolResult_HeadTailRatio(t *testing.T) {
	runes := make([]rune, 1000)
	for i := range runes {
		runes[i] = rune('a' + i%26)
	}
	content := string(runes)
	got := TruncateToolResult(content, 100)
	gotRunes := []rune(got)
	wantHead := runes[:60]
	wantTail := runes[len(runes)-40:]
	if string(gotRunes[:60]) != string(wantHead) {
		t.Fatalf("head mismatch: got %q, want %q", string(gotRunes[:60]), string(wantHead))
	}
	if string(gotRunes[len(gotRunes)-40:]) != string(wantTail) {
		t.Fatalf("tail mismatch: got %q, want %q", string(gotRunes[len(gotRunes)-40:]), string(wantTail))
	}
}

func TestTruncateToolResult_MarkerContainsOmittedCount(t *testing.T) {
	content := strings.Repeat("y", 1000)
	got := TruncateToolResult(content, 100)
	headChars := 100 * 60 / 100
	tailChars := 100 - headChars
	omitted := 1000 - headChars - tailChars
	want := fmt.Sprintf("…[truncated: %d chars omitted]…", omitted)
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want to contain %q", got, want)
	}
}

func TestTruncateToolResult_MultibyteBoundary_NoCorruption(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("🎉")
	}
	for i := 0; i < 200; i++ {
		b.WriteString("全")
	}
	content := b.String()
	got := TruncateToolResult(content, 100)
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid utf8: %q", got)
	}
	// head/tail 部分の rune が元の文字列の対応する rune と一致することを検証
	origRunes := []rune(content)
	gotRunes := []rune(got)
	headChars := 100 * 60 / 100
	tailChars := 100 - headChars
	for i := 0; i < headChars; i++ {
		if gotRunes[i] != origRunes[i] {
			t.Fatalf("head rune %d mismatch: got %q want %q", i, gotRunes[i], origRunes[i])
		}
	}
	for i := 0; i < tailChars; i++ {
		gi := len(gotRunes) - 1 - i
		oi := len(origRunes) - 1 - i
		if gotRunes[gi] != origRunes[oi] {
			t.Fatalf("tail rune %d mismatch: got %q want %q", i, gotRunes[gi], origRunes[oi])
		}
	}
}

func TestTruncateToolResult_MaxCharsZero_Disabled(t *testing.T) {
	content := strings.Repeat("z", 100000)
	got := TruncateToolResult(content, 0)
	if got != content {
		t.Fatalf("expected unchanged content when maxChars=0")
	}
}

func TestTruncateToolResult_MaxCharsNegative_Disabled(t *testing.T) {
	content := strings.Repeat("z", 100000)
	got := TruncateToolResult(content, -1)
	if got != content {
		t.Fatalf("expected unchanged content when maxChars=-1")
	}
}

func TestTruncateToolResult_EmptyContent(t *testing.T) {
	got := TruncateToolResult("", 100)
	if got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}
