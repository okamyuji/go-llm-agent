package ollama_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/ollama"
)

func TestChat_Sync(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message":           map[string]any{"role": "assistant", "content": "hello ollama"},
			"done":              true,
			"done_reason":       "stop",
			"prompt_eval_count": 2,
			"eval_count":        3,
		})
	}))
	defer srv.Close()

	cli := ollama.New(ollama.Options{BaseURL: srv.URL})
	res, err := cli.Chat(context.Background(), llm.ChatRequest{
		Model:    "llama3.1",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if res.Message.Content != "hello ollama" {
		t.Fatalf("content=%q", res.Message.Content)
	}
	if res.Usage.InputTokens != 2 || res.Usage.OutputTokens != 3 {
		t.Fatalf("usage=%+v", res.Usage)
	}
}

func TestStream_NDJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`{"message":{"role":"assistant","content":"hel"}}`,
			`{"message":{"role":"assistant","content":"lo"}}`,
			`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":2}`,
		}
		for _, line := range chunks {
			_, _ = w.Write([]byte(line + "\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	cli := ollama.New(ollama.Options{BaseURL: srv.URL})
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
