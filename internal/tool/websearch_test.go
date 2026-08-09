package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/config"
)

// ddgResultHTML は DDG HTML 応答の 1 結果ブロックを組み立てる
func ddgResultHTML(class, href, title, snippet string) string {
	return fmt.Sprintf(
		`<div class="%s"><h2 class="result__title"><a rel="nofollow" class="result__a" href="%s">%s</a></h2><a class="result__snippet" href="%s">%s</a></div>`,
		class, href, title, href, snippet)
}

func ddgPage(results ...string) string {
	return "<html><body><div id=\"links\">" + strings.Join(results, "") + "</div></body></html>"
}

func newSearchServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		if got := r.Header.Get("User-Agent"); !strings.Contains(got, "TestAgent") {
			t.Errorf("User-Agent not applied: %q", got)
		}
		if err := r.ParseForm(); err != nil || r.PostFormValue("q") == "" {
			t.Errorf("form q missing: err=%v", err)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runSearch(t *testing.T, srv *httptest.Server, args string) Result {
	t.Helper()
	ws := NewWebSearch(config.WebSearchToolConfig{
		Endpoint:  srv.URL,
		UserAgent: "TestAgent/1.0",
	})
	res, err := ws.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute err=%v", err)
	}
	return res
}

type searchOutput struct {
	Note    string `json:"note"`
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	} `json:"results"`
}

func parseSearchOutput(t *testing.T, res Result) searchOutput {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	var out searchOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("output is not JSON: %v (%s)", err, res.Content)
	}
	return out
}

func TestWebSearch_ExtractsResults(t *testing.T) {
	page := ddgPage(
		ddgResultHTML("result results_links web-result", "https://example.com/a", "Alpha", "first hit"),
		ddgResultHTML("result results_links web-result", "https://example.com/b", "Beta", "second hit"),
	)
	srv := newSearchServer(t, 200, page)
	out := parseSearchOutput(t, runSearch(t, srv, `{"query":"golang"}`))
	if len(out.Results) != 2 {
		t.Fatalf("want 2 results, got %d", len(out.Results))
	}
	if out.Results[0].Title != "Alpha" || out.Results[0].URL != "https://example.com/a" || out.Results[0].Snippet != "first hit" {
		t.Errorf("result[0] mismatch: %+v", out.Results[0])
	}
	if out.Note == "" {
		t.Errorf("note (未検証データ注記) がない")
	}
}

func TestWebSearch_ExcludesAds(t *testing.T) {
	page := ddgPage(
		ddgResultHTML("result result--ad", "https://ads.example.com", "Ad Title", "buy now"),
		ddgResultHTML("result web-result", "https://example.com/real", "Real", "organic"),
	)
	srv := newSearchServer(t, 200, page)
	out := parseSearchOutput(t, runSearch(t, srv, `{"query":"q"}`))
	if len(out.Results) != 1 || out.Results[0].Title != "Real" {
		t.Fatalf("ads must be excluded: %+v", out.Results)
	}
}

func TestWebSearch_DecodesUddgRedirect(t *testing.T) {
	real := "https://real.example/page?x=1"
	href := "//duckduckgo.com/l/?uddg=" + url.QueryEscape(real) + "&rut=abc"
	page := ddgPage(ddgResultHTML("result web-result", href, "Redirected", "s"))
	srv := newSearchServer(t, 200, page)
	out := parseSearchOutput(t, runSearch(t, srv, `{"query":"q"}`))
	if len(out.Results) != 1 || out.Results[0].URL != real {
		t.Fatalf("uddg not decoded: %+v", out.Results)
	}
}

func TestWebSearch_DropsNonHTTPScheme(t *testing.T) {
	href := "//duckduckgo.com/l/?uddg=" + url.QueryEscape("javascript:alert(1)")
	page := ddgPage(
		ddgResultHTML("result web-result", href, "Evil", "s"),
		ddgResultHTML("result web-result", "https://ok.example/", "OK", "s"),
	)
	srv := newSearchServer(t, 200, page)
	out := parseSearchOutput(t, runSearch(t, srv, `{"query":"q"}`))
	if len(out.Results) != 1 || out.Results[0].Title != "OK" {
		t.Fatalf("non-http scheme must be dropped: %+v", out.Results)
	}
}

func TestWebSearch_MaxResultsClamp(t *testing.T) {
	var blocks []string
	for i := range 8 {
		blocks = append(blocks, ddgResultHTML("result web-result", fmt.Sprintf("https://example.com/%d", i), fmt.Sprintf("T%d", i), "s"))
	}
	srv := newSearchServer(t, 200, ddgPage(blocks...))
	// config 既定 (MaxResults=0 → 既定 5)。max_results=100 は config 上限へクランプ
	out := parseSearchOutput(t, runSearch(t, srv, `{"query":"q","max_results":100}`))
	if len(out.Results) != 5 {
		t.Fatalf("want clamp to 5, got %d", len(out.Results))
	}
}

func TestWebSearch_ZeroResultsIsExplicitError(t *testing.T) {
	srv := newSearchServer(t, 200, "<html><body>no results markup</body></html>")
	res := runSearch(t, srv, `{"query":"q"}`)
	if !res.IsError || !strings.Contains(res.Content, "構造") {
		t.Fatalf("want structure-change error, got %+v", res)
	}
}

func TestWebSearch_BotChallenge202(t *testing.T) {
	srv := newSearchServer(t, 202, "challenge")
	res := runSearch(t, srv, `{"query":"q"}`)
	if !res.IsError || !strings.Contains(res.Content, "bot") {
		t.Fatalf("want bot-detection error for 202, got %+v", res)
	}
}

func TestWebSearch_EmptyQueryRejected(t *testing.T) {
	srv := newSearchServer(t, 200, ddgPage())
	res := runSearch(t, srv, `{"query":""}`)
	if !res.IsError {
		t.Fatalf("empty query must error: %+v", res)
	}
}
