package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/transport/httpapi"
)

func handlerOK(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
}

func mwGet(t *testing.T, h http.Handler, path string, headers map[string]string) *http.Response {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return res
}

func TestBearerAuth_AllowsKnownToken(t *testing.T) {
	t.Parallel()
	a := httpapi.NewBearerAuth(map[string]string{"abc": "local"}, "eyJ")
	res := mwGet(t, a.Handler(handlerOK(t)), "/v1/models", map[string]string{"Authorization": "Bearer abc"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d want 200", res.StatusCode)
	}
}

func TestBearerAuth_Rejects401WhenMissing(t *testing.T) {
	t.Parallel()
	a := httpapi.NewBearerAuth(map[string]string{"abc": "local"}, "eyJ")
	res := mwGet(t, a.Handler(handlerOK(t)), "/v1/models", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401", res.StatusCode)
	}
}

func TestBearerAuth_Rejects401WhenInvalid(t *testing.T) {
	t.Parallel()
	a := httpapi.NewBearerAuth(map[string]string{"abc": "local"}, "eyJ")
	res := mwGet(t, a.Handler(handlerOK(t)), "/v1/models", map[string]string{"Authorization": "Bearer wrong"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d want 401", res.StatusCode)
	}
}

func TestBearerAuth_DeferJWTPrefixPasses(t *testing.T) {
	t.Parallel()
	a := httpapi.NewBearerAuth(map[string]string{"abc": "local"}, "eyJ")
	res := mwGet(t, a.Handler(handlerOK(t)), "/v1/models", map[string]string{"Authorization": "Bearer eyJsomejwt"})
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("JWT-shaped token must pass-through, got %d", res.StatusCode)
	}
}

func TestBearerAuth_HealthzAlwaysOpen(t *testing.T) {
	t.Parallel()
	a := httpapi.NewBearerAuth(map[string]string{"abc": "local"}, "eyJ")
	res := mwGet(t, a.Handler(handlerOK(t)), "/healthz", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/healthz must be open, got %d", res.StatusCode)
	}
}

func TestBearerAuth_DisabledWhenEmpty(t *testing.T) {
	t.Parallel()
	a := httpapi.NewBearerAuth(nil, "eyJ")
	res := mwGet(t, a.Handler(handlerOK(t)), "/v1/models", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("empty tokens must disable auth, got %d", res.StatusCode)
	}
}

func TestTokenBucketLimiter_429AfterBurstExhausted(t *testing.T) {
	t.Parallel()
	l := httpapi.NewTokenBucketLimiter(1, 1, false)
	h := l.Handler(handlerOK(t))
	res1 := mwGet(t, h, "/v1/models", nil)
	_ = res1.Body.Close()
	res2 := mwGet(t, h, "/v1/models", nil)
	defer func() { _ = res2.Body.Close() }()
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("first req status = %d want 200", res1.StatusCode)
	}
	if res2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second req status = %d want 429", res2.StatusCode)
	}
}

func TestTokenBucketLimiter_HealthzSkipped(t *testing.T) {
	t.Parallel()
	l := httpapi.NewTokenBucketLimiter(1, 1, false)
	h := l.Handler(handlerOK(t))
	res1 := mwGet(t, h, "/healthz", nil)
	_ = res1.Body.Close()
	res2 := mwGet(t, h, "/healthz", nil)
	defer func() { _ = res2.Body.Close() }()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("/healthz must skip rate limit, got %d", res2.StatusCode)
	}
}

func TestTokenBucketLimiter_DisabledWhenZeroBurst(t *testing.T) {
	t.Parallel()
	if httpapi.NewTokenBucketLimiter(0, 0, false) != nil {
		t.Fatal("limiter must be nil when rps or burst is 0")
	}
}

func TestAllowlistCIDR_BlocksUnknownIP(t *testing.T) {
	t.Parallel()
	a, err := httpapi.NewAllowlistCIDR([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler(handlerOK(t))
	// httptest server は 127.0.0.1 から接続するため拒否されるべき
	res := mwGet(t, h, "/v1/models", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d want 403", res.StatusCode)
	}
}

func TestAllowlistCIDR_AllowsKnownIP(t *testing.T) {
	t.Parallel()
	a, err := httpapi.NewAllowlistCIDR([]string{"127.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler(handlerOK(t))
	res := mwGet(t, h, "/v1/models", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d want 200", res.StatusCode)
	}
}

func TestAllowlistCIDR_InvalidCIDR(t *testing.T) {
	t.Parallel()
	if _, err := httpapi.NewAllowlistCIDR([]string{"not-a-cidr"}); err == nil {
		t.Fatal("invalid cidr must error")
	}
}

func TestCORS_PreflightReturns204(t *testing.T) {
	t.Parallel()
	c := httpapi.NewCORS(true, []string{"https://example.com"}, []string{"GET", "POST", "OPTIONS"}, []string{"Authorization"})
	h := c.Handler(handlerOK(t))
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodOptions, srv.URL+"/v1/models", http.NoBody)
	req.Header.Set("Origin", "https://example.com")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d want 204", res.StatusCode)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("ACAO = %q want https://example.com", got)
	}
}

func TestCORS_DisabledIsNoop(t *testing.T) {
	t.Parallel()
	if httpapi.NewCORS(false, nil, nil, nil) != nil {
		t.Fatal("disabled CORS must be nil")
	}
}

func TestBearerAuth_NilReceiverIsNoop(t *testing.T) {
	t.Parallel()
	var a *httpapi.BearerAuth
	res := mwGet(t, a.Handler(handlerOK(t)), "/v1/models", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("nil bearer auth must be noop, got %d", res.StatusCode)
	}
}

func TestTokenBucketLimiter_NilReceiverIsNoop(t *testing.T) {
	t.Parallel()
	var l *httpapi.TokenBucketLimiter
	res := mwGet(t, l.Handler(handlerOK(t)), "/v1/models", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("nil limiter must be noop, got %d", res.StatusCode)
	}
}

func TestAllowlistCIDR_NilReceiverIsNoop(t *testing.T) {
	t.Parallel()
	var a *httpapi.AllowlistCIDR
	res := mwGet(t, a.Handler(handlerOK(t)), "/v1/models", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("nil allowlist must be noop, got %d", res.StatusCode)
	}
}

func TestCORS_NilReceiverIsNoop(t *testing.T) {
	t.Parallel()
	var c *httpapi.CORS
	res := mwGet(t, c.Handler(handlerOK(t)), "/v1/models", nil)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("nil cors must be noop, got %d", res.StatusCode)
	}
}

func TestTokenBucketLimiter_PerTokenIsolated(t *testing.T) {
	t.Parallel()
	l := httpapi.NewTokenBucketLimiter(1, 1, true)
	h := l.Handler(handlerOK(t))
	r1 := mwGet(t, h, "/v1/models", map[string]string{"Authorization": "Bearer one"})
	_ = r1.Body.Close()
	r2 := mwGet(t, h, "/v1/models", map[string]string{"Authorization": "Bearer two"})
	defer func() { _ = r2.Body.Close() }()
	if r1.StatusCode != http.StatusOK || r2.StatusCode != http.StatusOK {
		t.Fatalf("per-token isolation broken: r1=%d r2=%d", r1.StatusCode, r2.StatusCode)
	}
}

func TestAllowlistCIDR_EmptyReturnsNil(t *testing.T) {
	t.Parallel()
	a, err := httpapi.NewAllowlistCIDR(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a != nil {
		t.Fatal("empty cidrs must yield nil allowlist")
	}
}
