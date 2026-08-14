// Package main 05-streaming.md 5.2 節 / 10-slash-commands.md 5.2 節の
// REPL 基本ターンとスラッシュコマンドを検証するフィクスチャ。
// D-17 に従い OpenAI 互換 SSE スタブを fixture 内の httptest サーバで立て、
// 実 LLM・実ネットワークに依存しない
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/openai"
	"github.com/okamyuji/go-llm-agent/internal/tool"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

// answerText 1 ターン目の応答。日本語と絵文字を含み、runeSafeWriter を通した
// 経路で欠落・破損しないことを検証する
const answerText = "了解しました。あ😀い"

const (
	stubInputTokens  = 7
	stubOutputTokens = 5
	stubModel        = "openai/stub"
)

// sseChunk OpenAI 互換ストリーミングチャンクの最小形
type sseChunk struct {
	Choices []sseChoice `json:"choices"`
	Usage   *sseUsage   `json:"usage,omitempty"`
}

type sseChoice struct {
	Delta        sseDelta `json:"delta"`
	FinishReason string   `json:"finish_reason,omitempty"`
}

type sseDelta struct {
	Content string `json:"content,omitempty"`
}

type sseUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// writeSSE 1 チャンクを data: 行として書き出す
func writeSSE(w http.ResponseWriter, c sseChunk) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// splitRunes s を n 個ずつの rune 群へ分割する。SSE チャンク境界を作るために使う
func splitRunes(s string, n int) []string {
	if n <= 0 {
		return []string{s}
	}
	rs := []rune(s)
	var out []string
	for i := 0; i < len(rs); i += n {
		end := min(i+n, len(rs))
		out = append(out, string(rs[i:end]))
	}
	return out
}

// newStubServer answerText を複数チャンクへ分割して流す OpenAI 互換 SSE スタブ
func newStubServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, part := range splitRunes(answerText, 3) {
			if err := writeSSE(w, sseChunk{Choices: []sseChoice{{Delta: sseDelta{Content: part}}}}); err != nil {
				return
			}
		}
		_ = writeSSE(w, sseChunk{
			Choices: []sseChoice{{FinishReason: "stop"}},
			Usage:   &sseUsage{PromptTokens: stubInputTokens, CompletionTokens: stubOutputTokens},
		})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

// newOptions stub サーバへ向けた REPL オプションを返す
func newOptions(baseURL string, in string) (cliui.Options, llm.Registry) {
	client := openai.New(openai.Options{BaseURL: baseURL, APIKey: "stub"})
	reg := llm.NewRegistry(map[string]llm.Provider{"openai": client})
	return cliui.Options{
		Model:           stubModel,
		In:              strings.NewReader(in),
		DisableSpinner:  true,
		Registry:        reg,
		AvailableModels: map[string][]string{"openai": {"stub"}},
	}, reg
}

// runREPL 1 回の REPL 起動をエミュレートし、出力を返す
func runREPL(ctx context.Context, baseURL, in string) (string, error) {
	opt, reg := newOptions(baseURL, in)
	var buf bytes.Buffer
	opt.Out = &buf
	svc := agent.New(reg, tool.NewRegistry(nil, nil))
	err := cliui.NewREPL(svc, opt).Run(ctx)
	return buf.String(), err
}

// checkDeltaScenario 日本語と絵文字を含む応答が欠落も破損もなく出力されることを確認する
func checkDeltaScenario(ctx context.Context, baseURL string, out io.Writer) error {
	got, err := runREPL(ctx, baseURL, "こんにちは\n")
	if err != nil {
		return fmt.Errorf("delta シナリオの Run: %w", err)
	}
	if !strings.Contains(got, answerText) {
		return fmt.Errorf("応答文字列が出力に現れない: %q", got)
	}
	if !utf8.ValidString(got) {
		return fmt.Errorf("出力が妥当な UTF-8 でない")
	}
	fmt.Fprintln(out, "repl_delta_utf8_ok=true")
	return nil
}

// checkSlashScenario 1 ターン実行後に /help /model /cost を送り出力形式を確認する
func checkSlashScenario(ctx context.Context, baseURL string, out io.Writer) error {
	got, err := runREPL(ctx, baseURL, "こんにちは\n/help\n/model\n/cost\n")
	if err != nil {
		return fmt.Errorf("slash シナリオの Run: %w", err)
	}
	for _, want := range []string{"/model [provider/name]", "/compact", "/cost"} {
		if !strings.Contains(got, want) {
			return fmt.Errorf("/help の出力に %q が無い: %q", want, got)
		}
	}
	fmt.Fprintln(out, "repl_help_ok=true")
	if !strings.Contains(got, "[model] 現在のモデル: "+stubModel) {
		return fmt.Errorf("/model の出力に現在のモデルが無い: %q", got)
	}
	fmt.Fprintln(out, "repl_model_ok=true")
	wantCost := fmt.Sprintf("[cost] このセッション: in %d / out %d tok", stubInputTokens, stubOutputTokens)
	if !strings.Contains(got, wantCost) {
		return fmt.Errorf("/cost の出力に %q が無い: %q", wantCost, got)
	}
	fmt.Fprintln(out, "repl_cost_ok=true")
	return nil
}

func run(ctx context.Context, out io.Writer) error {
	srv := newStubServer()
	defer srv.Close()
	if err := checkDeltaScenario(ctx, srv.URL, out); err != nil {
		return err
	}
	return checkSlashScenario(ctx, srv.URL, out)
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "repl_basic_exercise:", err)
		os.Exit(1)
	}
}
