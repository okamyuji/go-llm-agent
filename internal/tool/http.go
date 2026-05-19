package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

// HTTPFetchTool http_fetch ツールの実装
type HTTPFetchTool struct {
	cfg  config.HTTPFetchToolConfig
	http *http.Client
}

// NewHTTPFetch config から HTTPFetchTool を生成する
func NewHTTPFetch(cfg config.HTTPFetchToolConfig) *HTTPFetchTool {
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 15
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 2 << 20
	}
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}
	if cfg.DenyPrivateNetworks {
		transport.DialContext = privateDenyDialer(10 * time.Second)
	}
	return &HTTPFetchTool{
		cfg: cfg,
		http: &http.Client{
			Timeout:   time.Duration(cfg.TimeoutSeconds) * time.Second,
			Transport: transport,
		},
	}
}

// Spec ツール定義を返す
func (t *HTTPFetchTool) Spec() Spec {
	return Spec{
		Name:        "http_fetch",
		Description: "公開 URL を GET してレスポンスボディをテキストとして返す。プライベートネットワークは拒否",
		Schema: json.RawMessage(`{
"type":"object",
"properties":{"url":{"type":"string"}},
"required":["url"]
}`),
	}
}

type httpFetchArgs struct {
	URL string `json:"url"`
}

// Execute 指定 URL を GET し、本文を返す
func (t *HTTPFetchTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a httpFetchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if a.URL == "" {
		return Result{IsError: true, Content: "url is required"}, nil
	}
	u, err := url.Parse(a.URL)
	if err != nil || !strings.HasPrefix(strings.ToLower(u.Scheme), "http") {
		return Result{IsError: true, Content: "url must be http(s)"}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	req.Header.Set("User-Agent", "go-llm-agent/1.0")
	res, err := t.http.Do(req)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	defer func() { _ = res.Body.Close() }()
	limited := io.LimitReader(res.Body, int64(t.cfg.MaxBodyBytes)+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	truncated := false
	if len(b) > t.cfg.MaxBodyBytes {
		b = b[:t.cfg.MaxBodyBytes]
		truncated = true
	}
	return Result{Content: fmt.Sprintf("HTTP %d\n%s", res.StatusCode, string(b)), Truncated: truncated}, nil
}

func privateDenyDialer(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if isPrivateOrLocal(ip) {
				return nil, fmt.Errorf("http_fetch: private or loopback IP rejected: %s", ip)
			}
		}
		return d.DialContext(ctx, network, addr)
	}
}

func isPrivateOrLocal(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	return false
}
