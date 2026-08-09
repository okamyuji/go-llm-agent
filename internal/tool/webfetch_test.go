package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

// writeStubWebgrab は引数を argsFile へ記録し body を stdout へ出して code で終了するスタブを作る
func writeStubWebgrab(t *testing.T, dir, body string, code int) (stubPath, argsFile string) {
	t.Helper()
	argsFile = filepath.Join(dir, "args.txt")
	stubPath = filepath.Join(dir, "webgrab-stub")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\ncat <<'BODY'\n%s\nBODY\nexit %d\n", argsFile, body, code)
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stubPath, argsFile
}

func stubJSON(markdown string, totalChars int) string {
	b, _ := json.Marshal(map[string]any{
		"markdown":       markdown,
		"untrusted":      true,
		"untrusted_note": "DO-NOT-LEAK-THIS-NOTE",
		"total_chars":    totalChars,
	})
	return string(b)
}

func runFetch(t *testing.T, cfg config.WebFetchToolConfig, args string) Result {
	t.Helper()
	res, err := NewWebFetch(cfg, nil).Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	return res
}

func TestWebFetch_ReturnsMarkdownWithPagingGuidance(t *testing.T) {
	dir := t.TempDir()
	stub, _ := writeStubWebgrab(t, dir, stubJSON("# Body text", 9000), 0)
	res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: stub, MaxChars: 4000},
		`{"url":"https://example.com/page"}`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "# Body text") {
		t.Errorf("markdown missing: %q", res.Content)
	}
	if !strings.Contains(res.Content, "start_index=4000") || !strings.Contains(res.Content, "9000") {
		t.Errorf("paging guidance missing: %q", res.Content)
	}
	if strings.Contains(res.Content, "DO-NOT-LEAK-THIS-NOTE") {
		t.Errorf("untrusted_note must not be forwarded (§6.2): %q", res.Content)
	}
}

func TestWebFetch_NoPagingGuidanceWhenComplete(t *testing.T) {
	dir := t.TempDir()
	stub, _ := writeStubWebgrab(t, dir, stubJSON("short", 5), 0)
	res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: stub, MaxChars: 4000},
		`{"url":"https://example.com/"}`)
	if res.IsError || strings.Contains(res.Content, "start_index=") {
		t.Fatalf("no paging guidance expected: %+v", res)
	}
}

func TestWebFetch_ArgsContainTerminatorAndLimits(t *testing.T) {
	dir := t.TempDir()
	stub, argsFile := writeStubWebgrab(t, dir, stubJSON("x", 1), 0)
	// max_chars=99999 は config 上限 4000 へクランプ、start_index=-5 は 0 へクランプ
	runFetch(t, config.WebFetchToolConfig{WebgrabPath: stub, MaxChars: 4000, TimeoutSeconds: 30},
		`{"url":"https://example.com/","max_chars":99999,"start_index":-5}`)
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--max-chars 4000") {
		t.Errorf("max_chars clamp missing: %v", args)
	}
	if !strings.Contains(joined, "--start-index 0") {
		t.Errorf("start_index clamp missing: %v", args)
	}
	if strings.Contains(joined, "--allow-private") {
		t.Errorf("--allow-private must never be passed: %v", args)
	}
	// URL は "--" の直後
	if len(args) < 2 || args[len(args)-2] != "--" || args[len(args)-1] != "https://example.com/" {
		t.Errorf("URL must follow -- terminator: %v", args)
	}
}

func TestWebFetch_ExitCodeMapping(t *testing.T) {
	tests := []struct {
		code      int
		wantWords []string
	}{
		{1, []string{"内部エラー"}},
		{2, []string{"URL"}},
		{3, []string{"リトライ可"}},
		{5, []string{"robots"}},
		{8, []string{"SSRF"}},
		{9, []string{"リトライ可"}}, // 未知コードはタイムアウト等扱い
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("exit%d", tt.code), func(t *testing.T) {
			dir := t.TempDir()
			stub, _ := writeStubWebgrab(t, dir, "boom", tt.code)
			res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: stub, MaxChars: 4000},
				`{"url":"https://example.com/"}`)
			if !res.IsError {
				t.Fatalf("exit %d must be error", tt.code)
			}
			for _, w := range tt.wantWords {
				if !strings.Contains(res.Content, w) {
					t.Errorf("exit %d: want %q in %q", tt.code, w, res.Content)
				}
			}
		})
	}
}

func TestWebFetch_TimeoutKillsProcess(t *testing.T) {
	dir := t.TempDir()
	stubPath := filepath.Join(dir, "webgrab-stub")
	// --timeout を無視して長時間スリープするスタブ
	if err := os.WriteFile(stubPath, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	wf := NewWebFetch(config.WebFetchToolConfig{WebgrabPath: stubPath, MaxChars: 4000, TimeoutSeconds: 1}, nil)
	wf.deadlineMargin = 0 // テストでは T+5s のマージンを外し 1 秒で kill させる
	res, err := wf.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/"}`))
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "タイムアウト") {
		t.Fatalf("want timeout error, got %+v", res)
	}
}

func TestWebFetch_SchemeRejectedBeforeExec(t *testing.T) {
	res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: "/nonexistent", MaxChars: 4000},
		`{"url":"ftp://example.com/"}`)
	if !res.IsError || !strings.Contains(res.Content, "http") {
		t.Fatalf("want scheme error, got %+v", res)
	}
}

func TestWebFetch_AllowDomainsRejectedBeforeExec(t *testing.T) {
	res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: "/nonexistent", MaxChars: 4000, AllowDomains: []string{"example.com"}},
		`{"url":"https://evil.example.org/"}`)
	if !res.IsError || !strings.Contains(res.Content, "allow_domains") {
		t.Fatalf("want allow_domains error, got %+v", res)
	}
}

func TestWebFetch_AllowDomainsSuffixMatch(t *testing.T) {
	dir := t.TempDir()
	stub, _ := writeStubWebgrab(t, dir, stubJSON("ok", 2), 0)
	res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: stub, MaxChars: 4000, AllowDomains: []string{"example.com"}},
		`{"url":"https://sub.example.com/"}`)
	if res.IsError {
		t.Fatalf("subdomain must be allowed (末尾一致): %+v", res)
	}
}

func TestWebFetch_BinaryMissing(t *testing.T) {
	res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: "/no/such/webgrab", MaxChars: 4000},
		`{"url":"https://example.com/"}`)
	if !res.IsError || !strings.Contains(res.Content, "README") {
		t.Fatalf("want install-hint error, got %+v", res)
	}
}

func TestWebFetch_BrokenJSONFromStdout(t *testing.T) {
	dir := t.TempDir()
	stub, _ := writeStubWebgrab(t, dir, "{not json", 0)
	res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: stub, MaxChars: 4000},
		`{"url":"https://example.com/"}`)
	if !res.IsError {
		t.Fatalf("broken JSON must error: %+v", res)
	}
}

func TestWebFetch_HostlessURLRejected(t *testing.T) {
	for _, u := range []string{"https:///path", "http:opaque"} {
		res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: "/nonexistent", MaxChars: 4000},
			fmt.Sprintf(`{"url":%q}`, u))
		if !res.IsError || !strings.Contains(res.Content, "http") {
			t.Fatalf("hostless url %q must be rejected before exec: %+v", u, res)
		}
	}
}

func TestWebFetch_StartFailureDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	// LookPath は通るが起動に失敗する (存在しないインタプリタの shebang)
	stub := filepath.Join(dir, "webgrab-bad")
	if err := os.WriteFile(stub, []byte("#!/nonexistent/interp\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := runFetch(t, config.WebFetchToolConfig{WebgrabPath: stub, MaxChars: 4000},
		`{"url":"https://example.com/"}`)
	if !res.IsError {
		t.Fatalf("start failure must return error result: %+v", res)
	}
}

func TestWebFetch_AuditLogOmitsQueryAndUserinfo(t *testing.T) {
	dir := t.TempDir()
	stub, _ := writeStubWebgrab(t, dir, stubJSON("x", 1), 0)
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	wf := NewWebFetch(config.WebFetchToolConfig{WebgrabPath: stub, MaxChars: 4000}, logger)
	_, err := wf.Execute(context.Background(), json.RawMessage(`{"url":"https://user:secret@example.com/path?token=tkn123"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "secret") || strings.Contains(got, "tkn123") {
		t.Errorf("audit log must omit userinfo and query: %s", got)
	}
	if !strings.Contains(got, "example.com/path") {
		t.Errorf("audit log should keep host+path: %s", got)
	}
}
