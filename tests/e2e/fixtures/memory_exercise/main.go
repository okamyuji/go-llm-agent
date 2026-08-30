// Package main 18-memory.md の自動メモリを検証するフィクスチャ。
// (1) REPL の # プレフィックスが memories.md と索引へ保存すること、
// (2) 索引を cmd/agent と同じ規則でシステムプロンプトへ合成すると stub provider が
// その内容を見ること (再起動後の注入に相当)、
// (3) memory_write / memory_read ツールが Store を介して往復できること、を
// 実 LLM に依存しない in-process スクリプト provider 方式で確認する。
// cmd/agent の非公開関数は import できないため、同等の合成をこのフィクスチャ内で行う
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/memory"
	"github.com/okamyuji/go-llm-agent/internal/tool"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// memoryFact 保存・注入の確認に使う目印の文
const memoryFact = "ユーザーは E2E 検証用のメモを覚えている"

// composeMemoryPrompt cmd/agent.resolveMemory と同じ合成規則
// (信頼境界マーカーで索引を囲む)
func composeMemoryPrompt(base, index string) string {
	return base + "\n\n[AGENT MEMORY] 以下は過去のセッションのメモの索引です。\n---- MEMORY.md ここから ----\n" +
		index + "\n---- MEMORY.md ここまで ----\n"
}

// seenStream 応答本文を 1 回だけ返すストリーム
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

// seenProvider 先頭 (system) メッセージに memoryFact が含まれていれば
// "memory_seen=true" を、無ければ "memory_seen=false" を応答本文に含める
type seenProvider struct{}

func (seenProvider) Name() string { return "stub" }
func (seenProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, fmt.Errorf("Chat は使わない (Stream のみ)")
}
func (seenProvider) Stream(_ context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	seen := len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, memoryFact)
	return &seenStream{text: fmt.Sprintf("memory_seen=%v", seen)}, nil
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

// runREPL 入力 input で REPL を実行し標準出力を返す
func runREPL(ctx context.Context, sysPrompt, input string, st *memory.Store) string {
	var out bytes.Buffer
	r := cliui.NewREPL(newSvc(), cliui.Options{
		Model: "stub/model", SystemPrompt: sysPrompt, MemoryStore: st,
		In: strings.NewReader(input), Out: &out, DisableSpinner: true,
	})
	if err := r.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "REPL run err:", err)
		os.Exit(1)
	}
	return out.String()
}

// checkHashSaves `# <本文>` が memories.md と索引 MEMORY.md へ保存されることを確認する
func checkHashSaves(ctx context.Context, st *memory.Store, w io.Writer) {
	runREPL(ctx, "base instructions", "# "+memoryFact+"\n/quit\n", st)
	body, err := st.Read("memories.md", 1<<20)
	index, ierr := st.ReadIndex(200, 24576)
	ok := err == nil && ierr == nil && strings.Contains(body, memoryFact) && strings.Contains(index, memoryFact)
	fmt.Fprintf(w, "memory_hash_saved=%v\n", ok)
}

// checkIndexInjected 索引を合成したシステムプロンプトが stub provider に見えることを確認する
func checkIndexInjected(ctx context.Context, st *memory.Store, w io.Writer) {
	index, err := st.ReadIndex(200, 24576)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ReadIndex err:", err)
		os.Exit(1)
	}
	sysPrompt := "base instructions"
	if index != "" {
		sysPrompt = composeMemoryPrompt(sysPrompt, index)
	}
	out := runREPL(ctx, sysPrompt, "hi\n/quit\n", st)
	fmt.Fprintf(w, "memory_index_injected=%v\n", strings.Contains(out, "memory_seen=true"))
}

// checkToolRoundTrip memory_write で書いた内容を memory_read で読み戻せることを確認する
func checkToolRoundTrip(ctx context.Context, st *memory.Store, w io.Writer) {
	mw := &tool.MemoryWriteTool{Store: st}
	mr := &tool.MemoryReadTool{Store: st}
	wres, werr := mw.Execute(ctx, json.RawMessage(`{"path":"topic.md","content":"ROUNDTRIP"}`))
	rres, rerr := mr.Execute(ctx, json.RawMessage(`{"path":"topic.md"}`))
	ok := werr == nil && rerr == nil && !wres.IsError && !rres.IsError && strings.Contains(rres.Content, "ROUNDTRIP")
	fmt.Fprintf(w, "memory_tool_roundtrip=%v\n", ok)
}

func main() {
	ctx := context.Background()
	var out bytes.Buffer

	dir, err := os.MkdirTemp("", "memory-exercise-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "MkdirTemp err:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	st, err := memory.NewStore(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "NewStore err:", err)
		os.Exit(1)
	}

	checkHashSaves(ctx, st, &out)
	checkIndexInjected(ctx, st, &out)
	checkToolRoundTrip(ctx, st, &out)

	fmt.Print(out.String())
}
