// Package main REPL の /image が llamacpp provider 経由で画像を data URI の content parts として送ることを検証するフィクスチャ。
// OpenAI 互換 SSE スタブを fixture 内の httptest サーバで立て、実 LLM・実ネットワークに依存しない
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
	"path/filepath"
	"strings"
	"sync"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/llamacpp"
	"github.com/okamyuji/go-llm-agent/internal/tool"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

const stubModel = "llamacpp/stub"

// pngBytes PNG シグネチャと IHDR チャンク頭。http.DetectContentType が image/png と判定する最小の中身
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// recorder スタブが受けたリクエストのうち、最後の user メッセージの content を記録する
type recorder struct {
	mu       sync.Mutex
	calls    int
	contents []any
}

func (r *recorder) record(body io.Reader) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			r.contents = append(r.contents, req.Messages[i].Content)
			return
		}
	}
}

func newStubServer(rec *recorder) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"赤と青です\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

// hasImagePart content が parts 配列で、data:image/png;base64, で始まる image_url を含むかを返す
func hasImagePart(content any) bool {
	parts, ok := content.([]any)
	if !ok {
		return false
	}
	for _, p := range parts {
		m, ok := p.(map[string]any)
		if !ok || m["type"] != "image_url" {
			continue
		}
		u, _ := m["image_url"].(map[string]any)
		if s, _ := u["url"].(string); strings.HasPrefix(s, "data:image/png;base64,") {
			return true
		}
	}
	return false
}

func run(ctx context.Context, out io.Writer) error {
	rec := &recorder{}
	srv := newStubServer(rec)
	defer srv.Close()

	dir, err := os.MkdirTemp("", "image-input-e2e")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	img := filepath.Join(dir, "a.png")
	if err := os.WriteFile(img, pngBytes, 0o600); err != nil {
		return err
	}

	reg := llm.NewRegistry(map[string]llm.Provider{"llamacpp": llamacpp.New(llamacpp.Options{BaseURL: srv.URL})})
	var buf bytes.Buffer
	in := "/image " + img + " 何色ですか\n/image " + filepath.Join(dir, "missing.png") + " q\n/quit\n"
	opt := cliui.Options{
		Model:           stubModel,
		In:              strings.NewReader(in),
		Out:             &buf,
		DisableSpinner:  true,
		Registry:        reg,
		AvailableModels: map[string][]string{"llamacpp": {"stub"}},
	}
	if err := cliui.NewREPL(agent.New(reg, tool.NewRegistry(nil, nil)), opt).Run(ctx); err != nil {
		return fmt.Errorf("REPL Run: %w", err)
	}
	got := buf.String()

	rec.mu.Lock()
	calls, contents := rec.calls, rec.contents
	rec.mu.Unlock()
	fmt.Fprintf(out, "image_parts_sent=%t\n", len(contents) == 1 && hasImagePart(contents[0]))
	fmt.Fprintf(out, "image_turns=%d\n", calls)
	fmt.Fprintf(out, "missing_image_reported=%t\n", strings.Contains(got, "[image]"))
	fmt.Fprintf(out, "answer_shown=%t\n", strings.Contains(got, "赤と青です"))
	return nil
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "image_input_exercise:", err)
		os.Exit(1)
	}
}
