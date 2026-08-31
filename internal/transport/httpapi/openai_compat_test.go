package httpapi_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/transport/httpapi"
)

// captureService agent.Service を満たす偽実装。Run に渡された agent.Input を記録する
type captureService struct {
	mu  sync.Mutex
	got []string
}

func (c *captureService) Run(_ context.Context, in agent.Input, out chan<- agent.Event) error {
	c.mu.Lock()
	c.got = append(c.got, in.SessionID)
	c.mu.Unlock()
	final := llm.Message{Role: llm.RoleAssistant, Content: "ok"}
	out <- agent.Event{Kind: agent.EventFinal, Final: &final}
	return nil
}

func TestHandleChatSetsSessionIDFromHeaderOrUUID(t *testing.T) {
	svc := &captureService{}
	cfg := &config.Config{DefaultModel: "fake/m", Providers: map[string]config.ProviderConfig{"fake": {}}}
	srv := httptest.NewServer(httpapi.New(svc, cfg, nil).Handler())
	defer srv.Close()

	body := `{"model":"m","messages":[{"role":"user","content":"x"}]}`

	req1, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req1.Header.Set("X-Session-Id", "abc-123")
	mustDo(t, req1)

	req2, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req2.Header.Set("X-Session-Id", "../evil")
	mustDo(t, req2)

	req3, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	mustDo(t, req3)

	svc.mu.Lock()
	got := append([]string(nil), svc.got...)
	svc.mu.Unlock()

	if len(got) != 3 || got[0] != "abc-123" {
		t.Fatalf("got=%v", got)
	}
	if got[1] == "../evil" || got[1] == "" || got[2] == "" || got[1] == got[2] {
		t.Fatalf("invalid header must become a fresh uuid: %v", got)
	}
}

func mustDo(t *testing.T, req *http.Request) {
	t.Helper()
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
}
