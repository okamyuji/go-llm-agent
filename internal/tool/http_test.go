package tool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

func TestHTTPFetch_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("body"))
	}))
	defer srv.Close()
	// プライベートネットワーク拒否はオフで動作確認
	ht := tool.NewHTTPFetch(localHTTPConfig(t, srv))
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if res.IsError {
		t.Fatalf("error result: %s", res.Content)
	}
	if !strings.Contains(res.Content, "body") {
		t.Fatalf("content=%q", res.Content)
	}
}

func TestHTTPFetch_RejectsNonHTTP(t *testing.T) {
	ht := tool.NewHTTPFetch(config.HTTPFetchToolConfig{})
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"file:///etc/passwd"}`))
	if !res.IsError {
		t.Fatal("non http は IsError")
	}
}

func TestHTTPFetch_RejectsHTTPPrefixScheme(t *testing.T) {
	ht := tool.NewHTTPFetch(config.HTTPFetchToolConfig{})
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"httpx://example.com/"}`))
	if !res.IsError {
		t.Fatal("http/https 以外のスキームは IsError")
	}
}

func TestHTTPFetch_RejectsUserinfo(t *testing.T) {
	ht := tool.NewHTTPFetch(config.HTTPFetchToolConfig{})
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"https://user:pass@example.com/"}`))
	if !res.IsError {
		t.Fatal("userinfo を含む URL は IsError")
	}
}

func TestHTTPFetch_RejectsPrivate(t *testing.T) {
	ht := tool.NewHTTPFetch(config.HTTPFetchToolConfig{DenyPrivateNetworks: true, TimeoutSeconds: 5})
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"http://127.0.0.1:1/"}`))
	if !res.IsError {
		t.Fatal("private は IsError")
	}
}

func TestHTTPFetch_UntrustedWrapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()
	ht := tool.NewHTTPFetch(localHTTPConfig(t, srv))
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if res.IsError {
		t.Fatalf("err: %s", res.Content)
	}
	if !strings.Contains(res.Content, "[untrusted external content from") {
		t.Fatalf("untrusted ヘッダが必要: %s", res.Content)
	}
	if !strings.Contains(res.Content, "[end untrusted content]") {
		t.Fatalf("untrusted フッタが必要: %s", res.Content)
	}
	if !strings.Contains(res.Content, "payload") {
		t.Fatalf("payload を含むこと: %s", res.Content)
	}
}

func TestHTTPFetch_PrivateAccessRequiresExplicitDomain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should not be reached"))
	}))
	defer srv.Close()
	ht := tool.NewHTTPFetch(config.HTTPFetchToolConfig{
		DenyPrivateNetworks: false,
		TimeoutSeconds:      5,
		MaxBodyBytes:        1024,
	})
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if !res.IsError || !strings.Contains(res.Content, "private or loopback") {
		t.Fatalf("private network 無効化には明示ドメインが必要: %+v", res)
	}
}

func TestHTTPFetch_RedirectRevalidatesDomain(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("target"))
	}))
	defer target.Close()
	targetURL := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	defer start.Close()

	ht := tool.NewHTTPFetch(localHTTPConfig(t, start))
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"`+start.URL+`"}`))
	if !res.IsError || !strings.Contains(res.Content, "allow_domains") {
		t.Fatalf("redirect先にもallow_domainsを適用する必要がある: %+v", res)
	}
}

func TestHTTPFetch_DomainAllowlist_Reject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	ht := tool.NewHTTPFetch(config.HTTPFetchToolConfig{
		DenyPrivateNetworks: false,
		TimeoutSeconds:      5,
		MaxBodyBytes:        1024,
		AllowDomains:        []string{"example.com"},
	})
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if !res.IsError {
		t.Fatal("許可ドメイン外は IsError")
	}
	if !strings.Contains(res.Content, "allow_domains") {
		t.Fatalf("理由を含むこと: %s", res.Content)
	}
}

func TestHTTPFetch_DomainAllowlist_AcceptSubdomain(t *testing.T) {
	// httptest は IP アドレスで listen するため、allow_domains に IP を入れて検証
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	host := srv.Listener.Addr().(*net.TCPAddr).IP.String()
	ht := tool.NewHTTPFetch(config.HTTPFetchToolConfig{
		DenyPrivateNetworks: false,
		TimeoutSeconds:      5,
		MaxBodyBytes:        1024,
		AllowDomains:        []string{host},
	})
	res, _ := ht.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if res.IsError {
		t.Fatalf("一致ホストは許可されるべき: %s", res.Content)
	}
}

func TestHTTPFetch_AuditLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	ht := tool.NewHTTPFetchWithLogger(localHTTPConfig(t, srv), logger)
	_, _ = ht.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if !strings.Contains(buf.String(), `tool=http_fetch`) {
		t.Fatalf("audit ログ: %s", buf.String())
	}
}

func localHTTPConfig(t *testing.T, srv *httptest.Server) config.HTTPFetchToolConfig {
	t.Helper()
	host := srv.Listener.Addr().(*net.TCPAddr).IP.String()
	return config.HTTPFetchToolConfig{
		DenyPrivateNetworks: false,
		TimeoutSeconds:      5,
		MaxBodyBytes:        1024,
		AllowDomains:        []string{host},
	}
}
