package openai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/goleak"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/openai"
)

// TestMain パッケージ全テスト終了時に goroutine リークを検証する
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestChat_Sync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer KEY" {
			t.Fatalf("auth header missing")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "x", "object": "chat.completion",
			"choices": []any{map[string]any{
				"index": 0, "finish_reason": "stop",
				"message": map[string]any{
					"role": "assistant", "content": "hello world",
				},
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2},
		})
	}))
	defer srv.Close()

	cli := openai.New(openai.Options{BaseURL: srv.URL, APIKey: "KEY"})
	res, err := cli.Chat(context.Background(), llm.ChatRequest{
		Model:    "gpt-4.1-mini",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Message.Content != "hello world" {
		t.Fatalf("content=%q", res.Message.Content)
	}
	if res.Usage.InputTokens != 3 || res.Usage.OutputTokens != 2 {
		t.Fatalf("usage=%+v", res.Usage)
	}
}

func TestStream_SSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		writes := []string{
			`{"choices":[{"delta":{"content":"hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
		}
		for _, line := range writes {
			_, _ = w.Write([]byte("data: " + line + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	cli := openai.New(openai.Options{BaseURL: srv.URL, APIKey: "K"})
	stream, err := cli.Stream(context.Background(), llm.ChatRequest{Model: "x"})
	if err != nil {
		t.Fatalf("stream err=%v", err)
	}
	defer func() { _ = stream.Close() }()

	var combined string
	var finish string
	for {
		ev, ok := stream.Recv()
		if !ok {
			break
		}
		combined += ev.DeltaText
		if ev.Finish != "" {
			finish = ev.Finish
		}
	}
	if combined != "hello" {
		t.Fatalf("combined=%q", combined)
	}
	if finish != "stop" {
		t.Fatalf("finish=%q", finish)
	}
}

func TestChat_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limit"))
	}))
	defer srv.Close()
	cli := openai.New(openai.Options{BaseURL: srv.URL, APIKey: "K"})
	_, err := cli.Chat(context.Background(), llm.ChatRequest{Model: "x", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*llm.ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError got %T", err)
	}
	if pe.StatusCode != 429 || !pe.Retryable {
		t.Fatalf("statuscode/retryable %+v", pe)
	}
}

func TestChatDeclaresToolsWithParametersAndDescription(t *testing.T) {
	// ツール宣言は OpenAI 仕様どおり function.parameters に JSON Schema、
	// function.description に説明を送る。tool-call 用の "arguments" キーを流用してはならない。
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := openai.New(openai.Options{BaseURL: srv.URL, APIKey: "x"})
	if _, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}},
		Tools:    []llm.ToolSpec{{Name: "fs_read", Description: "Read a file", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", gotBody["tools"])
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if _, hasArgs := fn["arguments"]; hasArgs {
		t.Errorf("tool declaration must not use 'arguments', got %v", fn)
	}
	params, ok := fn["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("function.parameters missing, got %v", fn)
	}
	if params["type"] != "object" {
		t.Errorf("parameters.type = %v, want object", params["type"])
	}
	if fn["description"] != "Read a file" {
		t.Errorf("function.description = %v, want 'Read a file'", fn["description"])
	}
}
