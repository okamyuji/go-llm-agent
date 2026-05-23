package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/transport/httpapi"
)

// doGet noctx 対策のため NewRequestWithContext で GET する
func doGet(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return res
}

// doPost noctx 対策のため NewRequestWithContext で POST する
func doPost(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return res
}

func TestUsageEndpoint_SessionScope(t *testing.T) {
	cfg := &config.Config{}
	pricing := map[string]billing.Pricing{
		"openai": {InputPerMillionJPY: 1, OutputPerMillionJPY: 1},
	}
	acc := billing.NewAccumulator(billing.Config{Pricing: pricing}, &fakeStore{})
	_, _ = acc.Add(context.Background(), "sess-1", "openai", "gpt", 100_000, 50_000)

	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg, acc).Handler())
	defer srv.Close()

	res := doGet(t, srv.URL+"/v1/usage?session=sess-1")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d want 200", res.StatusCode)
	}
	var body struct {
		Scope string `json:"scope"`
		Key   string `json:"key"`
		Total struct {
			InputTokens  int     `json:"input_tokens"`
			OutputTokens int     `json:"output_tokens"`
			CostJPY      float64 `json:"cost_jpy"`
		} `json:"total"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Scope != "session" || body.Key != "sess-1" {
		t.Errorf("scope=%q key=%q", body.Scope, body.Key)
	}
	if body.Total.InputTokens != 100_000 || body.Total.OutputTokens != 50_000 {
		t.Errorf("totals unexpected: %+v", body.Total)
	}
}

func TestUsageEndpoint_DateScope(t *testing.T) {
	cfg := &config.Config{}
	pricing := map[string]billing.Pricing{
		"openai": {InputPerMillionJPY: 1, OutputPerMillionJPY: 1},
	}
	acc := billing.NewAccumulator(billing.Config{Pricing: pricing}, &fakeStore{})
	_, _ = acc.Add(context.Background(), "sess-a", "openai", "gpt", 200_000, 100_000)

	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg, acc).Handler())
	defer srv.Close()

	res := doGet(t, srv.URL+"/v1/usage?date=2026-05-23")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d want 200", res.StatusCode)
	}
}

func TestUsageEndpoint_InvalidDate(t *testing.T) {
	cfg := &config.Config{}
	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg, nil).Handler())
	defer srv.Close()

	res := doGet(t, srv.URL+"/v1/usage?date=not-a-date")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d want 400", res.StatusCode)
	}
}

func TestUsageEndpoint_MissingQueryParams(t *testing.T) {
	cfg := &config.Config{}
	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg, nil).Handler())
	defer srv.Close()

	res := doGet(t, srv.URL+"/v1/usage")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d want 400", res.StatusCode)
	}
}

func TestUsageEndpoint_PostRejected(t *testing.T) {
	cfg := &config.Config{}
	srv := httptest.NewServer(httpapi.New(fakeSvc{}, cfg, nil).Handler())
	defer srv.Close()

	res := doPost(t, srv.URL+"/v1/usage")
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d want 405", res.StatusCode)
	}
}

// fakeStore Accumulator 専用の最小モック
type fakeStore struct{ items []billing.Snapshot }

func (s *fakeStore) Append(_ context.Context, snap billing.Snapshot) error {
	s.items = append(s.items, snap)
	return nil
}
func (s *fakeStore) QuerySession(_ context.Context, id string) ([]billing.Snapshot, error) {
	var out []billing.Snapshot
	for _, it := range s.items {
		if it.SessionID == id {
			out = append(out, it)
		}
	}
	return out, nil
}
func (s *fakeStore) QueryDate(_ context.Context, date string) ([]billing.Snapshot, error) {
	if date == "" {
		return s.items, nil
	}
	var out []billing.Snapshot
	for _, it := range s.items {
		if it.At.UTC().Format("2006-01-02") == date {
			out = append(out, it)
		}
	}
	return out, nil
}
