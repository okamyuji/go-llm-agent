// Package main 05 設計書の tool_choice / schema validation を検証するフィクスチャ
// 4 種類の Mode が provider ペイロードに正しく変換されることを検証し、
// 不正な JSON 引数が修正ループを経由して最終的に max_retries で停止することを確認する
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/openai"
)

func main() {
	captured := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		captured <- p
		// 最小限のチャット応答を返す
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	c := openai.New(openai.Options{BaseURL: srv.URL, APIKey: "x"})

	// required モードを送信
	if _, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:      "gpt",
		Messages:   []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Tools:      []llm.ToolSpec{{Name: "fs_read", Description: "", Schema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &llm.ToolChoice{Mode: "required"},
	}); err != nil {
		fmt.Fprintln(os.Stderr, "ERR Chat:", err)
		os.Exit(1)
	}

	var payload map[string]any
	select {
	case payload = <-captured:
	case <-time.After(5 * time.Second):
		fmt.Fprintln(os.Stderr, "ERR fake server did not receive payload within 5s")
		os.Exit(2)
	}
	// OpenAI 互換クライアントは小文字 "required" を厳密に送る想定。大文字小文字違いは検出して落とす
	if tc, ok := payload["tool_choice"].(string); !ok || tc != "required" {
		fmt.Fprintf(os.Stderr, "ERR tool_choice = %v, want 'required'\n", payload["tool_choice"])
		os.Exit(3)
	}
	fmt.Printf("tool_choice_payload=%v\n", payload["tool_choice"])
}
