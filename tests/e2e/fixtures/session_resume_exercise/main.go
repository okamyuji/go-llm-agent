// Package main 06-session-resume.md のセッション記録・-resume 復元を検証するフィクスチャ。
// cmdChat が呼ぶのと同じ公開関数 (cliui.ChatSessionsDir / cliui.ResumeLatestSession) を
// 直接呼び、実 LLM に依存しないスクリプト provider で 2 回の REPL 起動をエミュレートする
package main

import (
	"bytes"
	"context"
	"fmt"
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

func main() {
	ctx := context.Background()
	base, err := os.MkdirTemp("", "session-resume-exercise-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "MkdirTemp err:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(base) }()

	sessionsBase := filepath.Join(base, "sessions")
	chatDir := cliui.ChatSessionsDir("", sessionsBase)

	// 1. session1 実行
	runRepl(ctx, cliui.Options{Model: "stub/model", SessionsDir: chatDir}, session1Prompt+"\n/quit\n")

	// 2. ファイルが 1 つだけ作られていること
	fileCount := countJSONLFiles(chatDir)
	fmt.Printf("session1_file_created=%v\n", fileCount == 1)

	// 3. ChatSessionsDir のフォールバック規則
	wantChatDir := filepath.Join(sessionsBase, "chat")
	fmt.Printf("chat_dir_fallback_ok=%v\n", cliui.ChatSessionsDir("", sessionsBase) == wantChatDir)

	// 4. -resume の解釈経路
	latestID, ok, lerr := cliui.LatestSessionID(chatDir)
	var notified []string
	var warned []string
	notify := func(s string) { notified = append(notified, s) }
	warn := func(s string) { warned = append(warned, s) }
	sessionID, hist, rerr := cliui.ResumeLatestSession(chatDir, true, notify, warn)
	resumeOK := lerr == nil && ok && rerr == nil && sessionID == latestID && len(hist) == 2
	fmt.Printf("resume_flag_path_ok=%v\n", resumeOK)

	// 5. session2 が session1 のやり取りを参照できること
	out2 := runRepl(ctx, cliui.Options{
		Model: "stub/model", SessionsDir: chatDir, SessionID: sessionID, InitialHistory: hist,
	}, "hello-session2\n/quit\n")
	fmt.Printf("session2_sees_session1=%v\n", strings.Contains(out2, "resumed_ok"))

	// 6. 復元対象が無い空ディレクトリ
	emptyDir := filepath.Join(base, "empty-sessions")
	eid, ehist, eerr := cliui.ResumeLatestSession(emptyDir, true, func(string) {}, func(string) {})
	fmt.Printf("resume_empty_dir_ok=%v\n", eerr == nil && eid == "" && ehist == nil)

	// 7. 壊れた行を含む jsonl は読み飛ばして継続する
	brokenDir := filepath.Join(base, "broken-sessions")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir broken err:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(brokenDir, "20260101T000000Z.jsonl"),
		[]byte("{\"role\":\"user\",\"content\":\"a\"}\n{invalid json\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write broken err:", err)
		os.Exit(1)
	}
	var brokenWarned []string
	_, bhist, berr := cliui.ResumeLatestSession(brokenDir, true, func(string) {}, func(s string) { brokenWarned = append(brokenWarned, s) })
	fmt.Printf("broken_line_skipped=%v\n", berr == nil && len(bhist) == 1 && len(brokenWarned) == 1)
}
