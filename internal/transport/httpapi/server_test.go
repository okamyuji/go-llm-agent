package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/transport/httpapi"
)

type fakeSvc struct{}

func (fakeSvc) Run(_ context.Context, _ agent.Input, out chan<- agent.Event) error {
	out <- agent.Event{Kind: agent.EventDelta, Delta: "hi"}
	out <- agent.Event{Kind: agent.EventDelta, Delta: " world"}
	final := llm.Message{Role: llm.RoleAssistant, Content: "hi world"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final}
	return nil
}

func TestChat_NonStreaming(t *testing.T) {
	cfg := &config.Config{DefaultModel: "fake/m", Providers: map[string]config.ProviderConfig{"fake": {}}}
	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg).Handler())
	defer srv.Close()

	body := bytes.NewBufferString(`{"model":"fake/m","messages":[{"role":"user","content":"hi"}]}`)
	res, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Choices[0].Message.Content != "hi world" {
		t.Fatalf("content=%q", parsed.Choices[0].Message.Content)
	}
}

func TestChat_Streaming(t *testing.T) {
	cfg := &config.Config{DefaultModel: "fake/m", Providers: map[string]config.ProviderConfig{"fake": {}}}
	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg).Handler())
	defer srv.Close()

	body := bytes.NewBufferString(`{"model":"fake/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	res, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	out := string(b)
	if !strings.Contains(out, "data: ") {
		t.Fatalf("SSE 形式期待: %q", out)
	}
	if !strings.Contains(out, "[DONE]") {
		t.Fatalf("[DONE] 必要: %q", out)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("content missing: %q", out)
	}
}

func TestModels(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{"openai": {}, "ollama": {}}}
	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg).Handler())
	defer srv.Close()
	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "openai") {
		t.Fatalf("openai 含まれない: %s", string(b))
	}
}
