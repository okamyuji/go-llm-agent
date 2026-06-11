package ollama_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/goleak"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/ollama"
)

// TestMain パッケージ全テスト終了時に goroutine リークを検証する
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

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

func TestChat_TemperatureAndThink(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": "ok"},
			"done":    true,
		})
	}))
	defer srv.Close()

	temp := 0.0
	think := false
	cli := ollama.New(ollama.Options{BaseURL: srv.URL, Temperature: &temp, Think: &think})
	_, err := cli.Chat(context.Background(), llm.ChatRequest{
		Model:    "qwen3.5",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	opts, ok := captured["options"].(map[string]any)
	if !ok {
		t.Fatalf("options not sent: %+v", captured)
	}
	if got, ok := opts["temperature"].(float64); !ok || got != 0.0 {
		t.Errorf("temperature got=%v", opts["temperature"])
	}
	if got, ok := captured["think"].(bool); !ok || got != false {
		t.Errorf("think got=%v", captured["think"])
	}
}

func TestChat_NoInferenceOptionsOmitted(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": "ok"},
			"done":    true,
		})
	}))
	defer srv.Close()

	cli := ollama.New(ollama.Options{BaseURL: srv.URL})
	_, err := cli.Chat(context.Background(), llm.ChatRequest{
		Model:    "llama3.1",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if _, exists := captured["options"]; exists {
		t.Errorf("options should be omitted when unset: %+v", captured["options"])
	}
	if _, exists := captured["think"]; exists {
		t.Errorf("think should be omitted when unset: %+v", captured["think"])
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
