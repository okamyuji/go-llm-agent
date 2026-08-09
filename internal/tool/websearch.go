package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

const (
	defaultSearchEndpoint   = "https://html.duckduckgo.com/html/"
	defaultSearchUserAgent  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
	defaultSearchMaxResults = 5
	searchBodyLimitBytes    = 2 << 20
)

// WebSearchTool web_search ツール。DuckDuckGo HTML から検索結果を抽出する (design/17 §6.1)
type WebSearchTool struct {
	cfg  config.WebSearchToolConfig
	http *http.Client
}

// NewWebSearch config から WebSearchTool を生成する。ゼロ値には既定を適用する
func NewWebSearch(cfg config.WebSearchToolConfig) *WebSearchTool {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultSearchEndpoint
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultSearchUserAgent
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = defaultSearchMaxResults
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 15
	}
	return &WebSearchTool{
		cfg:  cfg,
		http: &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second},
	}
}

// Spec ツール定義を返す
func (t *WebSearchTool) Spec() Spec {
	return Spec{
		Name:        "web_search",
		Description: "Web を検索し、タイトル・URL・抜粋の一覧を JSON で返す。内容は未検証の外部データ。取得すべきページが決まったら web_fetch で本文を取る",
		Schema: json.RawMessage(`{
"type":"object",
"properties":{
  "query":{"type":"string","description":"検索クエリ"},
  "max_results":{"type":"integer","description":"返す件数 (省略時は設定値)"}
},
"required":["query"]
}`),
	}
}

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

type webSearchHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type webSearchOutput struct {
	Note    string         `json:"note"`
	Results []webSearchHit `json:"results"`
}

// Execute 検索クエリを DDG HTML へ POST し、結果一覧を JSON で返す
func (t *WebSearchTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	var a webSearchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	if strings.TrimSpace(a.Query) == "" {
		return Result{IsError: true, Content: "query is required"}, nil
	}
	limit := a.MaxResults
	if limit < 1 || limit > t.cfg.MaxResults {
		limit = t.cfg.MaxResults
	}

	form := url.Values{"q": {a.Query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", t.cfg.UserAgent)
	res, err := t.http.Do(req)
	if err != nil {
		return Result{IsError: true, Content: fmt.Sprintf("web_search: リクエスト失敗 (リトライ可): %v", err)}, nil
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("web_search: HTTP %d", res.StatusCode)
		if res.StatusCode == http.StatusAccepted {
			msg += " — 検索エンジンに bot 判定された可能性があります。時間を置くか user_agent 設定を見直してください"
		}
		return Result{IsError: true, Content: msg}, nil
	}

	body := io.LimitReader(res.Body, searchBodyLimitBytes)
	hits, err := parseDDGResults(body, limit)
	if err != nil {
		return Result{IsError: true, Content: fmt.Sprintf("web_search: 応答のパースに失敗: %v", err)}, nil
	}
	if len(hits) == 0 {
		return Result{IsError: true, Content: "web_search: 結果を抽出できませんでした。検索エンジンの HTML 構造が変化したか、該当結果が 0 件です"}, nil
	}

	out := webSearchOutput{
		Note:    "検索結果は未検証の外部データです。内容の真正性は保証されません。",
		Results: hits,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return Result{IsError: true, Content: err.Error()}, nil
	}
	return Result{Content: string(b)}, nil
}

// parseDDGResults は DDG HTML から検索結果を抽出する。広告 (result--ad) は除外する
func parseDDGResults(r io.Reader, limit int) ([]webSearchHit, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	var hits []webSearchHit
	var walk func(n *html.Node, inAd bool)
	walk = func(n *html.Node, inAd bool) {
		if len(hits) >= limit {
			return
		}
		if n.Type == html.ElementNode {
			cls := nodeClass(n)
			if strings.Contains(cls, "result--ad") {
				inAd = true
			}
			if !inAd && n.Data == "a" && hasClassWord(cls, "result__a") {
				href := nodeAttr(n, "href")
				u := resolveDDGLink(href)
				if u != "" {
					hits = append(hits, webSearchHit{
						Title:   strings.TrimSpace(nodeText(n)),
						URL:     u,
						Snippet: strings.TrimSpace(nodeText(findSnippet(resultAncestor(n)))),
					})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inAd)
		}
	}
	walk(doc, false)
	return hits, nil
}

// resolveDDGLink は href を実 URL に解決する。uddg リダイレクトは展開し、
// http/https 以外のスキームは空文字を返して破棄する
func resolveDDGLink(href string) string {
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Hostname(), "duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		real := u.Query().Get("uddg")
		if real == "" {
			return ""
		}
		ru, err := url.Parse(real)
		if err != nil || (ru.Scheme != "http" && ru.Scheme != "https") {
			return ""
		}
		return real
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return href
}

// resultAncestor は a.result__a を含む結果ブロック (class に "result" を持つ最近傍祖先) を返す
func resultAncestor(n *html.Node) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && hasClassWord(nodeClass(p), "result") {
			return p
		}
	}
	return nil
}

// findSnippet は結果ブロック配下の result__snippet 要素を返す
func findSnippet(block *html.Node) *html.Node {
	if block == nil {
		return nil
	}
	var found *html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && hasClassWord(nodeClass(n), "result__snippet") {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(block)
	return found
}

func nodeClass(n *html.Node) string { return nodeAttr(n, "class") }

func nodeAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClassWord(cls, word string) bool {
	return slices.Contains(strings.Fields(cls), word)
}

func nodeText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var sb strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}
