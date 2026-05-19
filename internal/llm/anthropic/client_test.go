package anthropic_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/anthropic"
)

func TestChat_Sync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "KEY" {
			t.Fatalf("api key header missing")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Fatalf("version header missing")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stop_reason": "end_turn",
			"content": []any{
				map[string]any{"type": "text", "text": "hello claude"},
			},
			"usage": map[string]any{"input_tokens": 5, "output_tokens": 7},
		})
	}))
	defer srv.Close()

	cli := anthropic.New(anthropic.Options{BaseURL: srv.URL, APIKey: "KEY"})
	res, err := cli.Chat(context.Background(), llm.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Message.Content != "hello claude" {
		t.Fatalf("content=%q", res.Message.Content)
	}
	if res.Usage.InputTokens != 5 || res.Usage.OutputTokens != 7 {
		t.Fatalf("usage=%+v", res.Usage)
	}
	if res.FinishReason != "end_turn" {
		t.Fatalf("finish reason")
	}
}

func TestStream_SSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		events := []string{
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}
		for _, line := range events {
			_, _ = w.Write([]byte(line + "\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	cli := anthropic.New(anthropic.Options{BaseURL: srv.URL, APIKey: "K"})
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
	if finish != "end_turn" {
		t.Fatalf("finish=%q", finish)
	}
}
