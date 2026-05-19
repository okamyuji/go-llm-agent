package tool_test

import (
	"context"
	"encoding/json"
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
