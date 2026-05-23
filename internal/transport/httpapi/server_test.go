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
	"github.com/okamyuji/go-llm-agent/internal/safety"
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
	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg, nil).Handler())
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
	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg, nil).Handler())
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

// piiChunkedSvc PII (メールアドレス) を意図的に chunk 境界で分割して送出する fake Service
// support@example.com を 3 つの EventDelta に分けて送ることで、loop の chunk 単位 redact では
// 取りこぼされ、syncChat 集約後の再 redact でのみ補正される状況を再現する
type piiChunkedSvc struct{}

func (piiChunkedSvc) Run(_ context.Context, _ agent.Input, out chan<- agent.Event) error {
	out <- agent.Event{Kind: agent.EventDelta, Delta: "お問い合わせ先は sup"}
	out <- agent.Event{Kind: agent.EventDelta, Delta: "port@exa"}
	out <- agent.Event{Kind: agent.EventDelta, Delta: "mple.com です"}
	final := llm.Message{Role: llm.RoleAssistant, Content: "お問い合わせ先は support@example.com です"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final}
	return nil
}

// emailRedactor テスト用の最小 PIIRedactor (email 1 ルール)
func emailRedactor(t *testing.T) safety.Redactor {
	t.Helper()
	pii, err := safety.NewPIIRedactor(safety.PIIRedactorConfig{
		Enabled: true,
		Rules: []safety.PIIRule{{
			ID:          "email",
			Regex:       `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`,
			Replacement: "[REDACTED:EMAIL]",
		}},
	})
	if err != nil {
		t.Fatalf("NewPIIRedactor: %v", err)
	}
	return pii
}

// TestChat_NonStreaming_PIIChunkCrossingRedacted Server に WithRedactor を渡せば
// chunk 境界を跨ぐ PII が non-stream の集約 content で確実に redact されることを確認する
// (実機検証で検出した HTTP non-stream のリーク回帰防止)
func TestChat_NonStreaming_PIIChunkCrossingRedacted(t *testing.T) {
	cfg := &config.Config{DefaultModel: "fake/m", Providers: map[string]config.ProviderConfig{"fake": {}}}
	srv := httptest.NewServer(httpapi.New(piiChunkedSvc{}, cfg, nil).WithRedactor(emailRedactor(t)).Handler())
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
	content := parsed.Choices[0].Message.Content
	if strings.Contains(content, "support@example.com") {
		t.Fatalf("PII が漏れている: %q", content)
	}
	if !strings.Contains(content, "[REDACTED:EMAIL]") {
		t.Fatalf("redact マーカーが見当たらない: %q", content)
	}
}

// TestChat_NonStreaming_NoRedactor_NoOp Redactor 未設定時は chunk 跨ぎ PII がそのまま通る挙動 (後方互換)
// 既存利用者が WithRedactor を未呼の場合に signature 変更で挙動が変わらないことを担保する
func TestChat_NonStreaming_NoRedactor_NoOp(t *testing.T) {
	cfg := &config.Config{DefaultModel: "fake/m", Providers: map[string]config.ProviderConfig{"fake": {}}}
	srv := httptest.NewServer(httpapi.New(piiChunkedSvc{}, cfg, nil).Handler())
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
	content := parsed.Choices[0].Message.Content
	if !strings.Contains(content, "support@example.com") {
		t.Fatalf("Redactor 未設定なのに redact されている: %q", content)
	}
}

func TestModels(t *testing.T) {
	cfg := &config.Config{Providers: map[string]config.ProviderConfig{"openai": {}, "ollama": {}}}
	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg, nil).Handler())
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
