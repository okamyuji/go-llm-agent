package cliui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func TestMessageEntryRoundTrip_UserContentOnly(t *testing.T) {
	m := llm.Message{Role: llm.RoleUser, Content: "hello"}
	got, err := entryToMessage(messageToEntry(m))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got.Role != m.Role || got.Content != m.Content {
		t.Fatalf("got=%+v want=%+v", got, m)
	}
}

func TestMessageEntryRoundTrip_AssistantWithToolCalls(t *testing.T) {
	m := llm.Message{
		Role: llm.RoleAssistant, Content: "calling tool",
		ToolCalls: []llm.ToolCall{{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"command":"echo"}`)}},
	}
	got, err := entryToMessage(messageToEntry(m))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "c1" || got.ToolCalls[0].Name != "shell" {
		t.Fatalf("got=%+v", got.ToolCalls)
	}
	if string(got.ToolCalls[0].Arguments) != `{"command":"echo"}` {
		t.Fatalf("arguments=%s", got.ToolCalls[0].Arguments)
	}
}

func TestMessageEntryRoundTrip_ToolMessage(t *testing.T) {
	m := llm.Message{Role: llm.RoleTool, Content: "result", ToolCallID: "c1", Name: "shell"}
	got, err := entryToMessage(messageToEntry(m))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got.ToolCallID != "c1" || got.Name != "shell" {
		t.Fatalf("got=%+v", got)
	}
}

func TestEntryToMessage_UnknownRoleErrors(t *testing.T) {
	_, err := entryToMessage(sessionEntry{Role: "unknown", Content: "x"})
	if err == nil {
		t.Fatal("want error for unknown role")
	}
}

func TestSessionWriter_AppendWritesOneJSONLinePerCall(t *testing.T) {
	dir := t.TempDir()
	w := newSessionWriter(dir, "s1")
	if got := w.sessionID(); got != "s1" {
		t.Fatalf("sessionID() = %q, want s1", got)
	}
	if err := w.append(llm.Message{Role: llm.RoleUser, Content: "first"}); err != nil {
		t.Fatalf("append err=%v", err)
	}
	if err := w.append(llm.Message{Role: llm.RoleAssistant, Content: "second"}); err != nil {
		t.Fatalf("append err=%v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "s1.jsonl"))
	if err != nil {
		t.Fatalf("read err=%v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), string(b))
	}
	var e1 sessionEntry
	if err := json.Unmarshal([]byte(lines[0]), &e1); err != nil || e1.Content != "first" {
		t.Fatalf("line1 = %q err=%v", lines[0], err)
	}
	var e2 sessionEntry
	if err := json.Unmarshal([]byte(lines[1]), &e2); err != nil || e2.Content != "second" {
		t.Fatalf("line2 = %q err=%v", lines[1], err)
	}
}

func TestSessionWriter_RotateSwitchesToNewFileWithoutTouchingOld(t *testing.T) {
	dir := t.TempDir()
	w := newSessionWriter(dir, "s1")
	if err := w.append(llm.Message{Role: llm.RoleUser, Content: "before"}); err != nil {
		t.Fatalf("append err=%v", err)
	}
	newID := w.rotate()
	if newID == "s1" {
		t.Fatal("rotate should produce a different id")
	}
	if err := w.append(llm.Message{Role: llm.RoleUser, Content: "after"}); err != nil {
		t.Fatalf("append err=%v", err)
	}
	oldB, err := os.ReadFile(filepath.Join(dir, "s1.jsonl"))
	if err != nil {
		t.Fatalf("read old err=%v", err)
	}
	if strings.Contains(string(oldB), "after") {
		t.Fatal("old file must not be touched by rotate")
	}
	newB, err := os.ReadFile(filepath.Join(dir, newID+".jsonl"))
	if err != nil {
		t.Fatalf("read new err=%v", err)
	}
	if !strings.Contains(string(newB), "after") {
		t.Fatalf("new file should contain the post-rotate entry: %s", newB)
	}
}

func TestNewSessionID_CollisionGetsSuffix(t *testing.T) {
	dir := t.TempDir()
	first := newSessionID(dir)
	if err := os.WriteFile(filepath.Join(dir, first+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("seed file err=%v", err)
	}
	second := newSessionID(dir)
	if second == first {
		t.Fatal("second id should differ after collision")
	}
	if !strings.HasPrefix(second, first) || !strings.Contains(second, "-2") {
		t.Fatalf("second=%q should be first with -2 suffix (or later)", second)
	}
}

func TestNewSessionID_UnreadableDirReturnsFallbackWithinTimeout(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root では chmod 0o000 が効かない")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "locked")
	if err := os.Mkdir(sub, 0o000); err != nil {
		t.Fatalf("mkdir err=%v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	done := make(chan string, 1)
	go func() { done <- newSessionID(sub) }()
	select {
	case id := <-done:
		if id == "" {
			t.Fatal("want non-empty fallback id")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("newSessionID did not return within timeout (possible infinite loop)")
	}
}

func TestNewSessionID_NonDirPathReturnsFallbackWithinTimeout(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write err=%v", err)
	}
	sub := filepath.Join(filePath, "child")

	done := make(chan string, 1)
	go func() { done <- newSessionID(sub) }()
	select {
	case id := <-done:
		if id == "" {
			t.Fatal("want non-empty fallback id")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("newSessionID did not return within timeout (possible infinite loop)")
	}
}

func TestLatestSessionID_NoFiles(t *testing.T) {
	dir := t.TempDir()
	_, ok, err := latestSessionID(dir)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestLatestSessionID_ReturnsLexicographicallyLargest(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"20260101T000000Z", "20260301T000000Z", "20260201T000000Z"} {
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("seed err=%v", err)
		}
	}
	id, ok, err := latestSessionID(dir)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if id != "20260301T000000Z" {
		t.Fatalf("id=%q want 20260301T000000Z", id)
	}
}

func TestLatestSessionID_DirDoesNotExist(t *testing.T) {
	_, ok, err := latestSessionID(filepath.Join(t.TempDir(), "missing"))
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func writeJSONLLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write err=%v", err)
	}
}

func TestLoadSession_ThreeValidLines(t *testing.T) {
	dir := t.TempDir()
	writeJSONLLines(t, filepath.Join(dir, "s1.jsonl"), []string{
		`{"ts":"2026-01-01T00:00:00Z","role":"user","content":"a"}`,
		`{"ts":"2026-01-01T00:00:01Z","role":"assistant","content":"b"}`,
		`{"ts":"2026-01-01T00:00:02Z","role":"user","content":"c"}`,
	})
	var warned []string
	msgs, err := loadSession(dir, "s1", func(s string) { warned = append(warned, s) })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(msgs) != 3 || msgs[0].Content != "a" || msgs[1].Content != "b" || msgs[2].Content != "c" {
		t.Fatalf("msgs=%+v", msgs)
	}
	if len(warned) != 0 {
		t.Fatalf("warn should not be called, got %v", warned)
	}
}

func TestLoadSession_SkipsBrokenJSONLine(t *testing.T) {
	dir := t.TempDir()
	writeJSONLLines(t, filepath.Join(dir, "s1.jsonl"), []string{
		`{"role":"user","content":"a"}`,
		`{invalid json`,
		`{"role":"user","content":"c"}`,
	})
	var warned []string
	msgs, err := loadSession(dir, "s1", func(s string) { warned = append(warned, s) })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "a" || msgs[1].Content != "c" {
		t.Fatalf("msgs=%+v", msgs)
	}
	if len(warned) != 1 || !strings.Contains(warned[0], ":2 ") {
		t.Fatalf("warned=%v", warned)
	}
}

func TestLoadSession_SkipsUnknownRole(t *testing.T) {
	dir := t.TempDir()
	writeJSONLLines(t, filepath.Join(dir, "s1.jsonl"), []string{
		`{"role":"user","content":"a"}`,
		`{"role":"alien","content":"b"}`,
	})
	var warned []string
	msgs, err := loadSession(dir, "s1", func(s string) { warned = append(warned, s) })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "a" {
		t.Fatalf("msgs=%+v", msgs)
	}
	if len(warned) != 1 {
		t.Fatalf("warned=%v", warned)
	}
}

func TestLoadSession_FileDoesNotExist(t *testing.T) {
	_, err := loadSession(t.TempDir(), "missing", func(string) {})
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestChatSessionsDir_ExplicitValueWins(t *testing.T) {
	if got := ChatSessionsDir("/explicit", "/sessions"); got != "/explicit" {
		t.Fatalf("got=%q", got)
	}
}

func TestChatSessionsDir_EmptyFallsBackToSessionsDirSlashChat(t *testing.T) {
	want := filepath.Join("/sessions", "chat")
	if got := ChatSessionsDir("", "/sessions"); got != want {
		t.Fatalf("got=%q want=%q", got, want)
	}
}

func TestResumeLatestSession_ResumeFalseReturnsEmpty(t *testing.T) {
	id, hist, err := ResumeLatestSession(t.TempDir(), false, func(string) {}, func(string) {})
	if err != nil || id != "" || hist != nil {
		t.Fatalf("id=%q hist=%v err=%v", id, hist, err)
	}
}

func TestResumeLatestSession_NoSessionsNotifiesAndReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	var notified []string
	id, hist, err := ResumeLatestSession(dir, true, func(s string) { notified = append(notified, s) }, func(string) {})
	if err != nil || id != "" || hist != nil {
		t.Fatalf("id=%q hist=%v err=%v", id, hist, err)
	}
	if len(notified) != 1 {
		t.Fatalf("notified=%v", notified)
	}
}

func TestResumeLatestSession_LoadsLatestAndNotifiesCount(t *testing.T) {
	dir := t.TempDir()
	writeJSONLLines(t, filepath.Join(dir, "20260101T000000Z.jsonl"), []string{`{"role":"user","content":"old"}`})
	writeJSONLLines(t, filepath.Join(dir, "20260201T000000Z.jsonl"), []string{
		`{"role":"user","content":"a"}`,
		`{"role":"assistant","content":"b"}`,
	})
	var notified []string
	id, hist, err := ResumeLatestSession(dir, true, func(s string) { notified = append(notified, s) }, func(string) {})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if id != "20260201T000000Z" {
		t.Fatalf("id=%q", id)
	}
	if len(hist) != 2 {
		t.Fatalf("hist=%+v", hist)
	}
	if len(notified) != 1 || !strings.Contains(notified[0], "2") {
		t.Fatalf("notified=%v", notified)
	}
}

func TestResumeLatestSession_BrokenLineIsSkippedAndWarned(t *testing.T) {
	dir := t.TempDir()
	writeJSONLLines(t, filepath.Join(dir, "20260101T000000Z.jsonl"), []string{
		`{"role":"user","content":"a"}`,
		`{invalid json`,
	})
	var warned []string
	_, hist, err := ResumeLatestSession(dir, true, func(string) {}, func(s string) { warned = append(warned, s) })
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("hist=%+v", hist)
	}
	if len(warned) != 1 {
		t.Fatalf("warned=%v", warned)
	}
}

func TestLatestSessionID_And_LoadSession_PublicWrappers(t *testing.T) {
	dir := t.TempDir()
	writeJSONLLines(t, filepath.Join(dir, "20260101T000000Z.jsonl"), []string{`{"role":"user","content":"a"}`})
	id, ok, err := LatestSessionID(dir)
	if err != nil || !ok || id != "20260101T000000Z" {
		t.Fatalf("id=%q ok=%v err=%v", id, ok, err)
	}
	msgs, err := LoadSession(dir, id, func(string) {})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("msgs=%+v err=%v", msgs, err)
	}
}

// TestLoadSession_LargeLineWithinScannerBuffer sc.Buffer の上限 (16MB) 内で
// 大きな 1 行を読めることを確認する (bufio.Scanner の既定 64KB 上限による
// bufio.ErrTooLong 回帰の防止)
func TestLoadSession_LargeLineWithinScannerBuffer(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 200000)
	entry := sessionEntry{Role: "user", Content: big}
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal err=%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), append(b, '\n'), 0o600); err != nil {
		t.Fatalf("write err=%v", err)
	}
	msgs, err := loadSession(dir, "s1", func(string) {})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(msgs) != 1 || len(msgs[0].Content) != len(big) {
		t.Fatalf("len=%d want=%d", len(msgs[0].Content), len(big))
	}
}
