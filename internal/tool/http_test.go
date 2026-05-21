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
	ht := tool.NewHTTPFetch(config.HTTPFetchToolConfig{DenyPrivateNetworks: false, TimeoutSeconds: 5, MaxBodyBytes: 1024})
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
	ht := tool.NewHTTPFetch(config.HTTPFetchToolConfig{DenyPrivateNetworks: false, TimeoutSeconds: 5, MaxBodyBytes: 1024})
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
	ht := tool.NewHTTPFetchWithLogger(config.HTTPFetchToolConfig{
		DenyPrivateNetworks: false,
		TimeoutSeconds:      5,
		MaxBodyBytes:        1024,
	}, logger)
	_, _ = ht.Execute(context.Background(), json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if !strings.Contains(buf.String(), `tool=http_fetch`) {
		t.Fatalf("audit ログ: %s", buf.String())
	}
}
