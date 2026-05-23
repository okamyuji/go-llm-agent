package gemini_test

import (
	"context"
	"encoding/json"
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
