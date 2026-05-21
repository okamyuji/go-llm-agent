package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

// HTTPFetchTool http_fetch ツールの実装
type HTTPFetchTool struct {
	cfg          config.HTTPFetchToolConfig
	http         *http.Client
	logger       *slog.Logger
	allowDomains []string
}

// NewHTTPFetch config から HTTPFetchTool を生成する
func NewHTTPFetch(cfg config.HTTPFetchToolConfig) *HTTPFetchTool {
	return NewHTTPFetchWithLogger(cfg, nil)
}

// NewHTTPFetchWithLogger config と logger を受け取り HTTPFetchTool を生成する
func NewHTTPFetchWithLogger(cfg config.HTTPFetchToolConfig, logger *slog.Logger) *HTTPFetchTool {
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
	doms := make([]string, 0, len(cfg.AllowDomains))
	for _, d := range cfg.AllowDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			doms = append(doms, d)
		}
	}
	return &HTTPFetchTool{
		cfg: cfg,
		http: &http.Client{
			Timeout:   time.Duration(cfg.TimeoutSeconds) * time.Second,
			Transport: transport,
		},
		logger:       logger,
		allowDomains: doms,
	}
}

// Spec ツール定義を返す
func (t *HTTPFetchTool) Spec() Spec {
	return Spec{
		Name:        "http_fetch",
		Description: "公開 URL を GET してレスポンスボディをテキストとして返す。プライベートネットワークと許可外ドメインは拒否、コンテンツは untrusted 標識付き",
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

// Execute 指定 URL を GET し、untrusted 標識を付けて本文を返す
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
	host := strings.ToLower(u.Hostname())
	if !t.hostAllowed(host) {
		t.audit(ctx, a.URL, host, 0, 0, false, "denied_domain")
		return Result{IsError: true, Content: fmt.Sprintf("http_fetch: host %q は allow_domains に含まれていません", host)}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	req.Header.Set("User-Agent", "go-llm-agent/1.0")
	res, err := t.http.Do(req)
	if err != nil {
		t.audit(ctx, a.URL, host, 0, 0, false, "dial_error")
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
	t.audit(ctx, a.URL, host, res.StatusCode, len(b), true, "ok")
	wrapped := wrapUntrusted(res.StatusCode, a.URL, string(b))
	return Result{Content: wrapped, Truncated: truncated}, nil
}

func (t *HTTPFetchTool) hostAllowed(host string) bool {
	if len(t.allowDomains) == 0 {
		return true
	}
	for _, suffix := range t.allowDomains {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func wrapUntrusted(status int, source, body string) string {
	return fmt.Sprintf(
		"[HTTP %d] [untrusted external content from %s]\n%s\n[end untrusted content]",
		status, source, body,
	)
}

func (t *HTTPFetchTool) audit(ctx context.Context, fullURL, host string, status, bytesLen int, ok bool, reason string) {
	if t.logger == nil {
		return
	}
	corr := ""
	if ctx != nil {
		if v, ok2 := ctx.Value(correlationKey{}).(string); ok2 {
			corr = v
		}
	}
	t.logger.Info("audit",
		slog.String("tool", "http_fetch"),
		slog.String("url", fullURL),
		slog.String("host", host),
		slog.Int("status", status),
		slog.Int("bytes", bytesLen),
		slog.Bool("ok", ok),
		slog.String("reason", reason),
		slog.String("correlation_id", corr),
	)
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
