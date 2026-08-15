// Package main 02-truncation.md のツール結果切り詰めを検証するフィクスチャ。
// 巨大なツール出力を実行し、履歴 (RoleTool メッセージ) が max_chars で
// 切り詰められること、EventToolResult で通知される本文は全文のままであること、
// max_chars=-1 (無効化) では切り詰めが起きないことを確認する
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// bigTool 固定長 (9000 rune) のダミー出力を返す。末尾に EXIT_CODE=1 を含め
// tail 40% 保持の確認に使う
type bigTool struct{ content string }

func (t bigTool) Spec() tool.Spec {
	return tool.Spec{Name: "big", Description: "big dummy output", Schema: json.RawMessage(`{"type":"object"}`)}
}

func (t bigTool) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: t.content}, nil
}

// scriptedStream は事前に用意した StreamEvent 列をそのまま返す
type scriptedStream struct {
	events []llm.StreamEvent
	i      int
}

func (s *scriptedStream) Recv() (llm.StreamEvent, bool) {
	if s.i >= len(s.events) {
		return llm.StreamEvent{}, false
	}
	ev := s.events[s.i]
	s.i++
	return ev, true
}
func (s *scriptedStream) Close() error { return nil }

// scriptedProvider は 1 回目の呼出しでツール呼出し、2 回目で最終応答を返す
type scriptedProvider struct{ call int }

func (p *scriptedProvider) Name() string { return "stub" }
func (p *scriptedProvider) Chat(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return nil, fmt.Errorf("Chat は使わない")
}
func (p *scriptedProvider) Stream(_ context.Context, _ llm.ChatRequest) (llm.ChatStream, error) {
	p.call++
	if p.call == 1 {
		return &scriptedStream{events: []llm.StreamEvent{
			{ToolCall: &llm.ToolCall{ID: "c1", Name: "big", Arguments: json.RawMessage(`{}`)}},
		}}, nil
	}
	return &scriptedStream{events: []llm.StreamEvent{{DeltaText: "done"}}}, nil
}

type stubRegistry struct{ p llm.Provider }

func (r stubRegistry) Resolve(model string) (llm.Provider, string, error) { return r.p, model, nil }
func (r stubRegistry) ResolveWithFallback(model string) (llm.Provider, string, llm.Provider, string, error) {
	return r.p, model, nil, "", nil
}
func (r stubRegistry) List() []string { return []string{"stub/model"} }

// runOnce svc を 1 ターン実行し、EventFinal.TurnMessages 中の RoleTool メッセージと
// EventToolResult の全文を返す
func runOnce(maxChars int, content string) (historyContent string, fullContent string, err error) {
	prov := &scriptedProvider{}
	reg := stubRegistry{p: prov}
	tools := tool.NewRegistry([]tool.Tool{bigTool{content: content}}, []string{"big"})
	svc := agent.New(reg, tools, agent.WithToolResultLimit(maxChars))

	out := make(chan agent.Event, 16)
	runErr := svc.Run(context.Background(), agent.Input{
		Model:       "stub/model",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "run big"}},
		MaxToolHops: 2,
	}, out)
	close(out)
	if runErr != nil {
		return "", "", runErr
	}
	return extractResults(out)
}

// extractResults out からツール結果の全文 (EventToolResult) と、EventFinal.TurnMessages
// 中の RoleTool メッセージ (履歴用、切り詰め済み) を取り出す
func extractResults(out <-chan agent.Event) (historyContent, fullContent string, err error) {
	final, fullContent := collectFinal(out)
	if final == nil {
		return "", "", fmt.Errorf("EventFinal not emitted")
	}
	return toolMessageContent(final.TurnMessages), fullContent, nil
}

// collectFinal out を最後まで読み、EventFinal と EventToolResult の全文を返す
func collectFinal(out <-chan agent.Event) (final *agent.Event, fullContent string) {
	for ev := range out {
		if ev.Kind == agent.EventToolResult && ev.ToolResult != nil {
			fullContent = ev.ToolResult.Content
		}
		if ev.Kind == agent.EventFinal {
			evCopy := ev
			final = &evCopy
		}
	}
	return final, fullContent
}

// toolMessageContent turnMessages 中の RoleTool メッセージの Content を返す
// (存在しなければ空文字列)
func toolMessageContent(turnMessages []llm.Message) string {
	for _, m := range turnMessages {
		if m.Role == llm.RoleTool {
			return m.Content
		}
	}
	return ""
}

// wrappedTarget loop.go が wrapUntrusted で付与するマーカーを含めた後の目標 rune 数。
//
// 逸脱: 02-truncation.md 5.4 節はツール生の出力を 9000 rune として書くが、
// loop.go は 06 番設計書により全ツール出力へ [UNTRUSTED INPUT: tool=...] / [END
// UNTRUSTED] マーカーを無条件付与してから EventToolResult / 履歴へ渡す
// (02 番設計書より後に導入された仕様、00-overview 3.2 節の「1 箇所」は
// wrapUntrusted 適用後の tr.Content を指す)。02 番の期待値
// (history_content_chars=8033 等) はラップ後の rune 数が 9000 であることを
// 前提に閉じた式で成立するため、ここではダミーツールの生出力をラップ後
// ちょうど 9000 rune になる長さへ調整する。EventToolResult / 履歴が扱う
// 実際の対象 (ラップ後の tr.Content) が 9000 rune になるという点で、
// 02 番設計書の検証意図と数値の一致は保たれる
const wrappedTarget = 9000

const (
	wrapPrefix = "[UNTRUSTED INPUT: tool=big]\n"
	wrapSuffix = "\n[END UNTRUSTED]"
	// exitSuffix 末尾に判別可能な終了コード文字列を含める (tail 40% 保持の確認用)
	exitSuffix = "\nEXIT_CODE=1"
)

// buildFixtureContent wrapUntrusted 適用後にちょうど wrappedTarget rune になる
// ダミーツール出力を組み立てる
func buildFixtureContent() (content string, wrappedChars int) {
	fillerChars := wrappedTarget - utf8.RuneCountInString(wrapPrefix) - utf8.RuneCountInString(wrapSuffix) - utf8.RuneCountInString(exitSuffix)
	var b strings.Builder
	b.WriteString(strings.Repeat("x", fillerChars))
	b.WriteString(exitSuffix)
	content = b.String()
	wrappedChars = utf8.RuneCountInString(wrapPrefix) + utf8.RuneCountInString(content) + utf8.RuneCountInString(wrapSuffix)
	return content, wrappedChars
}

// printEnabledResult history_content_chars 等、切詰め有効時のキーを標準出力へ書く
func printEnabledResult(historyContent, fullContent string) {
	fmt.Printf("history_content_chars=%d\n", utf8.RuneCountInString(historyContent))
	fmt.Printf("contains_marker=%v\n", strings.Contains(historyContent, "…[truncated:"))
	fmt.Printf("contains_tail_exit_code=%v\n", strings.Contains(historyContent, "EXIT_CODE=1"))
	fmt.Printf("event_tool_result_full_chars=%d\n", utf8.RuneCountInString(fullContent))
}

// mustRunOnce runOnce を実行し、失敗時は label 付きのエラーを stderr へ書いて
// exitCode で終了する
func mustRunOnce(maxChars int, content, label string, exitCode int) (historyContent, fullContent string) {
	historyContent, fullContent, err := runOnce(maxChars, content)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR "+label+":", err)
		os.Exit(exitCode)
	}
	return historyContent, fullContent
}

func main() {
	content, wrappedChars := buildFixtureContent()
	if wrappedChars != wrappedTarget {
		fmt.Fprintf(os.Stderr, "ERR fixture wrapped content length = %d, want %d\n", wrappedChars, wrappedTarget)
		os.Exit(1)
	}

	historyContent, fullContent := mustRunOnce(8000, content, "enabled run", 2)
	printEnabledResult(historyContent, fullContent)

	disabledHistory, _ := mustRunOnce(-1, content, "disabled run", 3)
	fmt.Printf("disabled_history_content_chars=%d\n", utf8.RuneCountInString(disabledHistory))
}
