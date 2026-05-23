package gemini_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/gemini"
)

// TestMain パッケージ全テスト終了時に goroutine リークを検証する
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestChat_ToolCall_RoundTripsThoughtSignature Gemini thinking model から
// 返ってきた thoughtSignature をツールコールに保持し、次の API リクエストに
// 同じ signature を含めて送り返す
func TestChat_ToolCall_RoundTripsThoughtSignature(t *testing.T) {
	// 1 回目: ツールコール（thoughtSignature 付き）を返す
	// 2 回目: 受け取った payload を検証して signature の往復を確認する
	var capturedBody []byte
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if turn == 0 {
			turn++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{map[string]any{
					"finishReason": "STOP",
					"content": map[string]any{
						"role": "model",
						"parts": []any{map[string]any{
							"functionCall": map[string]any{
								"name": "search_files",
								"args": map[string]any{"q": "x"},
							},
							"thoughtSignature": "SIG-ABC-XYZ",
						}},
					},
				}},
			})
			return
		}
		capturedBody = body
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{map[string]any{
				"finishReason": "STOP",
				"content": map[string]any{
					"role":  "model",
					"parts": []any{map[string]any{"text": "ok"}},
				},
			}},
		})
	}))
	defer srv.Close()

	cli := gemini.New(gemini.Options{BaseURL: srv.URL, APIKey: "K"})
	res1, err := cli.Chat(context.Background(), llm.ChatRequest{
		Model:    "gemini-3.5-flash",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "find files"}},
	})
	if err != nil {
		t.Fatalf("turn1 err=%v", err)
	}
	if len(res1.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(res1.Message.ToolCalls))
	}
	got := res1.Message.ToolCalls[0]
	if got.Metadata["thoughtSignature"] != "SIG-ABC-XYZ" {
		t.Fatalf("expected signature in Metadata, got %+v", got.Metadata)
	}

	// 2 回目: ツール結果と共に履歴を送り返す
	_, err = cli.Chat(context.Background(), llm.ChatRequest{
		Model: "gemini-3.5-flash",
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: "find files"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{got}},
			{Role: llm.RoleTool, Name: "search_files", Content: "[]"},
		},
	})
	if err != nil {
		t.Fatalf("turn2 err=%v", err)
	}
	if !bytes.Contains(capturedBody, []byte(`"thoughtSignature":"SIG-ABC-XYZ"`)) {
		t.Fatalf("thoughtSignature missing in turn2 payload: %s", string(capturedBody))
	}
}

// TestStream_ToolCall_CapturesThoughtSignature SSE のツールコールで
// thoughtSignature が ToolCall.Metadata に保持される
func TestStream_ToolCall_CapturesThoughtSignature(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunk := `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"search_files","args":{"q":"x"}},"thoughtSignature":"SIG-STREAM-001"}]},"finishReason":"STOP"}]}`
		_, _ = w.Write([]byte("data: " + chunk + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cli := gemini.New(gemini.Options{BaseURL: srv.URL, APIKey: "K"})
	stream, err := cli.Stream(context.Background(), llm.ChatRequest{Model: "gemini-3.5-flash"})
	if err != nil {
		t.Fatalf("stream err=%v", err)
	}
	defer func() { _ = stream.Close() }()

	var found *llm.ToolCall
	for {
		ev, ok := stream.Recv()
		if !ok {
			break
		}
		if ev.Err != nil {
			t.Fatalf("recv err=%v", ev.Err)
		}
		if ev.ToolCall != nil {
			found = ev.ToolCall
		}
	}
	if found == nil {
		t.Fatal("expected ToolCall event")
	}
	if found.Metadata[`thoughtSignature`] != "SIG-STREAM-001" {
		t.Fatalf("expected signature in Metadata, got %+v", found.Metadata)
	}
}

func TestChat_Sync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/models/") {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "KEY" {
			t.Fatalf("key query missing")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{
				map[string]any{
					"finishReason": "STOP",
					"content": map[string]any{
						"role":  "model",
						"parts": []any{map[string]any{"text": "hello gemini"}},
					},
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":     4,
				"candidatesTokenCount": 6,
			},
		})
	}))
	defer srv.Close()

	cli := gemini.New(gemini.Options{BaseURL: srv.URL, APIKey: "KEY"})
	res, err := cli.Chat(context.Background(), llm.ChatRequest{
		Model:    "gemini-2.5-pro",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Message.Content != "hello gemini" {
		t.Fatalf("content=%q", res.Message.Content)
	}
	if res.Usage.InputTokens != 4 || res.Usage.OutputTokens != 6 {
		t.Fatalf("usage=%+v", res.Usage)
	}
}

func TestStream_SSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"hel"}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP"}]}`,
		}
		for _, line := range chunks {
			_, _ = w.Write([]byte("data: " + line + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	cli := gemini.New(gemini.Options{BaseURL: srv.URL, APIKey: "K"})
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
	if finish != "STOP" {
		t.Fatalf("finish=%q", finish)
	}
}

func TestStream_EmitsUsageFromUsageMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}]}`,
			`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22}}`,
		}
		for _, line := range chunks {
			_, _ = w.Write([]byte("data: " + line + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	cli := gemini.New(gemini.Options{BaseURL: srv.URL, APIKey: "K"})
	stream, err := cli.Stream(context.Background(), llm.ChatRequest{Model: "x"})
	if err != nil {
		t.Fatalf("stream err=%v", err)
	}
	defer func() { _ = stream.Close() }()

	var sawUsage bool
	for {
		ev, ok := stream.Recv()
		if !ok {
			break
		}
		if ev.Usage != nil {
			if ev.Usage.InputTokens != 11 || ev.Usage.OutputTokens != 22 {
				t.Fatalf("usage=%+v", ev.Usage)
			}
			sawUsage = true
		}
	}
	if !sawUsage {
		t.Fatal("expected Stream to emit Usage from usageMetadata, got none")
	}
}
