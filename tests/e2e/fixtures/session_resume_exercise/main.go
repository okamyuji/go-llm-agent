// Package main 06-session-resume.md のセッション記録・-resume 復元を検証するフィクスチャ。
// cmdChat が呼ぶのと同じ公開関数 (cliui.ChatSessionsDir / cliui.ResumeLatestSession) を
// 直接呼び、実 LLM に依存しないスクリプト provider で 2 回の REPL 起動をエミュレートする
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

const session1Prompt = "hello-session1"

// stubStream 1 件の delta と usage を返すだけの最小 ChatStream
type stubStream struct {
	events []llm.StreamEvent
	i      int
}

func (s *stubStream) Recv() (llm.StreamEvent, bool) {
	if s.i >= len(s.events) {
		return llm.StreamEvent{}, false
	}
	ev := s.events[s.i]
	s.i++
	return ev, true
}
func (s *stubStream) Close() error { return nil }

// stubProvider 受け取った Messages に session1Prompt が含まれていれば
// "resumed_ok" を返す (2 回目の REPL 起動が 1 回目のやり取りを見ていることの確認)。
// 含まれていなければ通常の固定応答を返す
type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }
func (stubProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, fmt.Errorf("Chat は使わない (Stream のみ)")
}
func (stubProvider) Stream(_ context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	content := "session1 answer"
	for _, m := range req.Messages {
		if strings.Contains(m.Content, session1Prompt) && m.Role == llm.RoleUser {
			// 履歴の先頭近くに session1 の発話が見えている == resume 成功
			if hasEarlierTurn(req.Messages, m) {
				content = "resumed_ok"
			}
		}
	}
	return &stubStream{events: []llm.StreamEvent{
		{DeltaText: content},
		{Usage: &llm.Usage{InputTokens: 1, OutputTokens: 1}},
	}}, nil
}

// hasEarlierTurn m が req.Messages の最後の要素でなければ「以前のターン」とみなす
// (2 回目の REPL 起動では InitialHistory 由来の session1 発話が history の先頭に来る)
func hasEarlierTurn(all []llm.Message, m llm.Message) bool {
	if len(all) == 0 {
		return false
	}
	return all[len(all)-1].Content != m.Content || len(all) > 1
}

type stubRegistry struct{ p llm.Provider }

func (r stubRegistry) Resolve(model string) (llm.Provider, string, error) { return r.p, model, nil }
func (r stubRegistry) ResolveWithFallback(model string) (llm.Provider, string, llm.Provider, string, error) {
	return r.p, model, nil, "", nil
}
func (r stubRegistry) List() []string { return []string{"stub/model"} }

func newSvc() agent.Service {
	reg := stubRegistry{p: stubProvider{}}
	tools := tool.NewRegistry(nil, nil)
	return agent.New(reg, tools)
}

// runRepl 1 回の REPL 起動をエミュレートし、標準出力の内容を返す
func runRepl(ctx context.Context, opt cliui.Options, promptLines string) string {
	var out bytes.Buffer
	opt.In = strings.NewReader(promptLines)
	opt.Out = &out
	opt.DisableSpinner = true
	r := cliui.NewREPL(newSvc(), opt)
	if err := r.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "REPL run err:", err)
		os.Exit(1)
	}
	return out.String()
}

func countJSONLFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			n++
		}
	}
	return n
}

// checkSession1AndResume session1 を実行し記録・-resume 復元経路を検証する。
// session2 実行に必要な sessionID / hist も返す
func checkSession1AndResume(ctx context.Context, base string, w io.Writer) (sessionID string, hist []llm.Message) {
	sessionsBase := filepath.Join(base, "sessions")
	chatDir := cliui.ChatSessionsDir("", sessionsBase)

	runRepl(ctx, cliui.Options{Model: "stub/model", SessionsDir: chatDir}, session1Prompt+"\n/quit\n")

	fileCount := countJSONLFiles(chatDir)
	fmt.Fprintf(w, "session1_file_created=%v\n", fileCount == 1)

	wantChatDir := filepath.Join(sessionsBase, "chat")
	fmt.Fprintf(w, "chat_dir_fallback_ok=%v\n", cliui.ChatSessionsDir("", sessionsBase) == wantChatDir)

	latestID, ok, lerr := cliui.LatestSessionID(chatDir)
	sessionID, hist, rerr := cliui.ResumeLatestSession(chatDir, true, func(string) {}, func(string) {})
	resumeOK := lerr == nil && ok && rerr == nil && sessionID == latestID && len(hist) == 2
	fmt.Fprintf(w, "resume_flag_path_ok=%v\n", resumeOK)
	return sessionID, hist
}

// checkSession2SeesSession1 session2 を InitialHistory 付きで実行し、
// stubProvider が resumed_ok を返すことを確認する
func checkSession2SeesSession1(ctx context.Context, chatDir, sessionID string, hist []llm.Message, w io.Writer) {
	out2 := runRepl(ctx, cliui.Options{
		Model: "stub/model", SessionsDir: chatDir, SessionID: sessionID, InitialHistory: hist,
	}, "hello-session2\n/quit\n")
	fmt.Fprintf(w, "session2_sees_session1=%v\n", strings.Contains(out2, "resumed_ok"))
}

// checkResumeEmptyDir 復元対象が無い空ディレクトリでの ResumeLatestSession の挙動を検証する
func checkResumeEmptyDir(base string, w io.Writer) {
	emptyDir := filepath.Join(base, "empty-sessions")
	eid, ehist, eerr := cliui.ResumeLatestSession(emptyDir, true, func(string) {}, func(string) {})
	fmt.Fprintf(w, "resume_empty_dir_ok=%v\n", eerr == nil && eid == "" && ehist == nil)
}

// checkBrokenLineSkipped 壊れた行を含む jsonl が読み飛ばされ、警告が 1 回だけ呼ばれることを確認する
func checkBrokenLineSkipped(base string, w io.Writer) error {
	brokenDir := filepath.Join(base, "broken-sessions")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		return fmt.Errorf("mkdir broken: %w", err)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "20260101T000000Z.jsonl"),
		[]byte("{\"role\":\"user\",\"content\":\"a\"}\n{invalid json\n"), 0o600); err != nil {
		return fmt.Errorf("write broken: %w", err)
	}
	var brokenWarned []string
	_, bhist, berr := cliui.ResumeLatestSession(brokenDir, true, func(string) {}, func(s string) { brokenWarned = append(brokenWarned, s) })
	fmt.Fprintf(w, "broken_line_skipped=%v\n", berr == nil && len(bhist) == 1 && len(brokenWarned) == 1)
	return nil
}

// run 全チェックを順に実行し、標準出力へ key=value を書く。エラーがあれば返す
func run(ctx context.Context, base string, w io.Writer) error {
	sessionsBase := filepath.Join(base, "sessions")
	chatDir := cliui.ChatSessionsDir("", sessionsBase)

	sessionID, hist := checkSession1AndResume(ctx, base, w)
	checkSession2SeesSession1(ctx, chatDir, sessionID, hist, w)
	checkResumeEmptyDir(base, w)
	return checkBrokenLineSkipped(base, w)
}

func main() {
	base, err := os.MkdirTemp("", "session-resume-exercise-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "MkdirTemp err:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(base) }()

	if err := run(context.Background(), base, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "run err:", err)
		os.Exit(1)
	}
}
