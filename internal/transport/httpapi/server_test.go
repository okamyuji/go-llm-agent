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

	"go.uber.org/goleak"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/transport/httpapi"
)

// TestMain パッケージ全テスト終了時に goroutine リークを検証する
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

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
	res, err := postJSON(t, srv.URL+"/v1/chat/completions", body)
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
	res, err := postJSON(t, srv.URL+"/v1/chat/completions", body)
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
	res, err := getURL(t, srv.URL+"/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "openai") {
		t.Fatalf("openai 含まれない: %s", string(b))
	}
}

func postJSON(t *testing.T, url string, body io.Reader) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}

func getURL(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}
