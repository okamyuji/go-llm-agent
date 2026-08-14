// Package main 07-agents-md.md の AGENTS.md 自動読み込みを検証するフィクスチャ。
// agent.LoadAgentsMD / composeSystemPrompt (main パッケージの非公開関数は
// import できないため、同等の合成をこのフィクスチャ内で行う) を通し、
// stub provider が system メッセージへの反映を検証する。実 LLM に依存しない
// (06-session-resume.md のフィクスチャと同じ in-process スクリプト provider 方式)。
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

const agentsMDMarker = "必ず語尾に (AGENTS.md 適用済み)"

// composeSystemPrompt cmd/agent.composeSystemPrompt と同じ合成規則
// (信頼境界マーカーで AGENTS.md の内容を囲む)
func composeSystemPrompt(base, agentsMDContent string) string {
	return base + "\n\n[UNTRUSTED PROJECT FILE: AGENTS.md]\n---- AGENTS.md ここから ----\n" +
		agentsMDContent + "\n---- AGENTS.md ここまで ----\n"
}

// seenStream 受け取った Messages を検査し、先頭 (system) メッセージに
// agentsMDMarker が含まれていれば "agents_md_seen=true" を、無ければ
// "agents_md_seen=false" を応答本文に含める
type seenStream struct {
	sent bool
	text string
}

func (s *seenStream) Recv() (llm.StreamEvent, bool) {
	if s.sent {
		return llm.StreamEvent{}, false
	}
	s.sent = true
	return llm.StreamEvent{DeltaText: s.text}, true
}
func (s *seenStream) Close() error { return nil }

type seenProvider struct{}

func (seenProvider) Name() string { return "stub" }
func (seenProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, fmt.Errorf("Chat は使わない (Stream のみ)")
}
func (seenProvider) Stream(_ context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	seen := len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, agentsMDMarker)
	text := fmt.Sprintf("agents_md_seen=%v", seen)
	return &seenStream{text: text}, nil
}

type stubRegistry struct{ p llm.Provider }

func (r stubRegistry) Resolve(model string) (llm.Provider, string, error) { return r.p, model, nil }
func (r stubRegistry) ResolveWithFallback(model string) (llm.Provider, string, llm.Provider, string, error) {
	return r.p, model, nil, "", nil
}
func (r stubRegistry) List() []string { return []string{"stub/model"} }

func newSvc() agent.Service {
	return agent.New(stubRegistry{p: seenProvider{}}, tool.NewRegistry(nil, nil))
}

// runOnce 1 ターン分の REPL 実行を行い標準出力を返す
func runOnce(ctx context.Context, sysPrompt string) string {
	var out bytes.Buffer
	r := cliui.NewREPL(newSvc(), cliui.Options{
		Model: "stub/model", SystemPrompt: sysPrompt,
		In: strings.NewReader("hi\n/quit\n"), Out: &out, DisableSpinner: true,
	})
	if err := r.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "REPL run err:", err)
		os.Exit(1)
	}
	return out.String()
}

// checkAgentsMDPresent AGENTS.md を配置し合成したシステムプロンプトが
// stub provider に見えることを確認する
func checkAgentsMDPresent(ctx context.Context, dir string, w *bytes.Buffer) {
	agentsMDPath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agentsMDPath, []byte(agentsMDMarker+"を付けて回答してください"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write AGENTS.md err:", err)
		os.Exit(1)
	}
	roots := []string{dir}
	content, path, err := agent.LoadAgentsMD(dir, 0, roots)
	if err != nil {
		fmt.Fprintln(os.Stderr, "LoadAgentsMD err:", err)
		os.Exit(1)
	}
	if path == "" {
		fmt.Fprintln(w, "agents_md_prompt_applied=false detail=not_found")
		return
	}
	sysPrompt := composeSystemPrompt("base instructions", content)
	out := runOnce(ctx, sysPrompt)
	fmt.Fprintf(w, "agents_md_prompt_applied=%v\n", strings.Contains(out, "agents_md_seen=true"))
}

// checkAgentsMDAbsent AGENTS.md を配置しない場合、stub provider が
// マーカーを見ないことを確認する
func checkAgentsMDAbsent(ctx context.Context, dir string, w *bytes.Buffer) {
	roots := []string{dir}
	content, path, err := agent.LoadAgentsMD(dir, 0, roots)
	if err != nil {
		fmt.Fprintln(os.Stderr, "LoadAgentsMD err:", err)
		os.Exit(1)
	}
	sysPrompt := "base instructions"
	if path != "" {
		sysPrompt = composeSystemPrompt(sysPrompt, content)
	}
	out := runOnce(ctx, sysPrompt)
	fmt.Fprintf(w, "agents_md_absent_ok=%v\n", strings.Contains(out, "agents_md_seen=false"))
}

func main() {
	ctx := context.Background()
	var out bytes.Buffer

	presentDir, err := os.MkdirTemp("", "agents-md-present-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "MkdirTemp err:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(presentDir) }()
	checkAgentsMDPresent(ctx, presentDir, &out)

	absentDir, err := os.MkdirTemp("", "agents-md-absent-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "MkdirTemp err:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(absentDir) }()
	checkAgentsMDAbsent(ctx, absentDir, &out)

	fmt.Print(out.String())
}
