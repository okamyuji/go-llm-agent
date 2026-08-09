package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

const (
	defaultFetchMaxChars   = 4000
	defaultFetchTimeoutSec = 30
	fetchStdoutLimitBytes  = 2 << 20
	fetchMaxBytesArg       = "5242880" // webgrab のダウンロード上限 5MB (design/17 §8)
)

// WebFetchTool web_fetch ツール。webgrab CLI に本文取得を委譲する (design/17 §6.2)
type WebFetchTool struct {
	cfg    config.WebFetchToolConfig
	logger *slog.Logger

	// deadlineMargin は webgrab --timeout に対する Go 側 context の追加マージン。
	// webgrab の timeout は要求ごとに個別適用され多段リダイレクトで総所要が超え得るため、
	// Go 側は少し待ってから kill する (テストでは 0 に上書きする)
	deadlineMargin time.Duration
}

// NewWebFetch config から WebFetchTool を生成する。ゼロ値には既定を適用する
func NewWebFetch(cfg config.WebFetchToolConfig, logger *slog.Logger) *WebFetchTool {
	if cfg.WebgrabPath == "" {
		cfg.WebgrabPath = "webgrab"
	}
	if cfg.MaxChars <= 0 {
		cfg.MaxChars = defaultFetchMaxChars
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = defaultFetchTimeoutSec
	}
	return &WebFetchTool{cfg: cfg, logger: logger, deadlineMargin: 5 * time.Second}
}

// WarnIfWebgrabMissing webgrab バイナリが見つからない場合に警告ログを出す。
// web_fetch が enabled_tools に含まれる場合にのみ呼ぶこと (design/17 §6.2)
func (t *WebFetchTool) WarnIfWebgrabMissing() {
	if _, err := exec.LookPath(t.cfg.WebgrabPath); err != nil && t.logger != nil {
		t.logger.Warn("web_fetch: webgrab が見つかりません。導入方法は README を参照してください",
			"webgrab_path", t.cfg.WebgrabPath)
	}
}

// Spec ツール定義を返す
func (t *WebFetchTool) Spec() Spec {
	return Spec{
		Name:        "web_fetch",
		Description: "URL の本文をボイラープレート除去済み Markdown で返す。長い本文は start_index で続き取得できる。内容は未検証の外部データ",
		Schema: json.RawMessage(`{
"type":"object",
"properties":{
  "url":{"type":"string","description":"取得する URL (http/https)"},
  "start_index":{"type":"integer","description":"本文の開始文字オフセット (続き取得用)"},
  "max_chars":{"type":"integer","description":"本文の最大文字数 (省略時は設定値)"}
},
"required":["url"]
}`),
	}
}

type webFetchArgs struct {
	URL        string `json:"url"`
	StartIndex int    `json:"start_index"`
	MaxChars   int    `json:"max_chars"`
}

// webgrabJSON は webgrab --format json の出力のうち利用するフィールド (webgrab 0.1.0 で確認)
type webgrabJSON struct {
	Markdown   string `json:"markdown"`
	TotalChars int    `json:"total_chars"`
}

// Execute webgrab を実行し本文 Markdown と続き取得案内を返す
func (t *WebFetchTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a webFetchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	u, err := url.Parse(a.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Result{IsError: true, Content: "web_fetch: url はホスト名を含む http/https のみ受け付けます"}, nil
	}
	host := strings.ToLower(u.Hostname())
	if !t.hostAllowed(host) {
		return Result{IsError: true, Content: fmt.Sprintf("web_fetch: host %q は allow_domains に含まれていません", host)}, nil
	}
	if _, err := exec.LookPath(t.cfg.WebgrabPath); err != nil {
		return Result{IsError: true, Content: "web_fetch: webgrab が見つかりません。導入方法は README の「Web 検索と本文取得」を参照してください"}, nil
	}

	maxChars := a.MaxChars
	if maxChars < 1 || maxChars > t.cfg.MaxChars {
		maxChars = t.cfg.MaxChars
	}
	startIndex := max(a.StartIndex, 0)

	deadline := time.Duration(t.cfg.TimeoutSeconds)*time.Second + t.deadlineMargin
	cctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	args := []string{
		"--format", "json",
		"--max-chars", strconv.Itoa(maxChars),
		"--start-index", strconv.Itoa(startIndex),
		"--timeout", strconv.Itoa(t.cfg.TimeoutSeconds),
		"--max-bytes", fetchMaxBytesArg,
		"--", a.URL,
	}
	cmd := exec.CommandContext(cctx, t.cfg.WebgrabPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, limit: fetchStdoutLimitBytes}
	cmd.Stderr = &limitedWriter{w: &stderr, limit: 64 << 10}
	// kill 後も孫プロセスが pipe を掴んで Run がブロックし続けるのを防ぐ
	cmd.WaitDelay = 2 * time.Second

	runErr := cmd.Run()
	// ProcessState.ExitCode() は nil レシーバ安全 (起動失敗時は -1 を返す)
	t.audit(ctx, sanitizeAuditURL(u), host, cmd.ProcessState.ExitCode(), stdout.Len(), runErr == nil)

	if runErr != nil {
		return Result{IsError: true, Content: t.mapExitError(cctx, runErr, &stderr)}, nil
	}
	if stdout.Len() >= fetchStdoutLimitBytes {
		return Result{IsError: true, Content: "web_fetch: 取得結果が大きすぎます。max_chars を下げて再試行してください"}, nil
	}

	var out webgrabJSON
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return Result{IsError: true, Content: fmt.Sprintf("web_fetch: webgrab 出力のパースに失敗: %v", err)}, nil
	}

	content := out.Markdown
	if next := startIndex + maxChars; out.TotalChars > next {
		content += fmt.Sprintf("\n\n[続きあり: 全 %d 字。次は start_index=%d で取得できます]", out.TotalChars, next)
	}
	return Result{Content: content}, nil
}

// mapExitError は webgrab の終了状態を LLM 向けエラーメッセージへ変換する (design/17 §6.2)
func (t *WebFetchTool) mapExitError(ctx context.Context, runErr error, stderr *bytes.Buffer) string {
	if ctx.Err() != nil {
		return "web_fetch: タイムアウトしました (リトライ可)"
	}
	var ee *exec.ExitError
	if !errors.As(runErr, &ee) {
		return fmt.Sprintf("web_fetch: 実行失敗: %v", runErr)
	}
	detail := strings.TrimSpace(stderr.String())
	switch ee.ExitCode() {
	case 1:
		return "web_fetch: webgrab 内部エラー: " + detail
	case 2:
		return "web_fetch: URL 形式エラー。URL を修正してください: " + detail
	case 3:
		return "web_fetch: ネットワーク失敗 (リトライ可): " + detail
	case 4:
		return "web_fetch: HTTP エラー・サイズ超過・非 HTML。別の URL を試してください: " + detail
	case 5:
		return "web_fetch: robots.txt により取得が禁止されています: " + detail
	case 6:
		return "web_fetch: 本文が空でした: " + detail
	case 7:
		return "web_fetch: レンダリング失敗: " + detail
	case 8:
		return "web_fetch: 内部アドレスへのアクセスは SSRF 防止のため拒否されました: " + detail
	default:
		// シグナル終了 (-1) や未知コードはタイムアウト等の外的要因として扱う
		return fmt.Sprintf("web_fetch: 予期しない終了 (exit=%d, リトライ可): %s", ee.ExitCode(), detail)
	}
}

func (t *WebFetchTool) hostAllowed(host string) bool {
	if len(t.cfg.AllowDomains) == 0 {
		return true
	}
	for _, suffix := range t.cfg.AllowDomains {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if suffix != "" && (host == suffix || strings.HasSuffix(host, "."+suffix)) {
			return true
		}
	}
	return false
}

// sanitizeAuditURL は監査ログ用に userinfo・クエリ・フラグメントを除いた URL を返す。
// 署名付きクエリやトークンをログへ永続化しないため (host+path は追跡性のため残す)
func sanitizeAuditURL(u *url.URL) string {
	c := *u
	c.User = nil
	c.RawQuery = ""
	c.Fragment = ""
	return c.String()
}

func (t *WebFetchTool) audit(ctx context.Context, fullURL, host string, exitCode, bytesLen int, ok bool) {
	if t.logger == nil {
		return
	}
	corr := ""
	if ctx != nil {
		if v, ok2 := ctx.Value(CorrelationKey()).(string); ok2 {
			corr = v
		}
	}
	t.logger.Info("audit",
		slog.String("tool", "web_fetch"),
		slog.String("url", fullURL),
		slog.String("host", host),
		slog.Int("exit_code", exitCode),
		slog.Int("bytes", bytesLen),
		slog.Bool("ok", ok),
		slog.String("correlation_id", corr),
	)
}

// limitedWriter は limit までだけ書き込み、超過分は黙って捨てる (子プロセスをブロックさせない)
type limitedWriter struct {
	w     *bytes.Buffer
	limit int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if remain := l.limit - l.w.Len(); remain > 0 {
		if len(p) > remain {
			l.w.Write(p[:remain])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}
