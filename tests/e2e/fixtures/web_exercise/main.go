// Package main 17 設計書の web_search / web_fetch ツールの動作確認。
// 実インターネットへは出ず、httptest サーバとスタブ webgrab で検証する
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

const ddgFixture = `<html><body><div id="links">
<div class="result result--ad"><h2 class="result__title"><a class="result__a" href="https://ads.example/">Ad</a></h2><a class="result__snippet" href="#">buy</a></div>
<div class="result web-result"><h2 class="result__title"><a class="result__a" href="https://example.com/doc">Example Doc</a></h2><a class="result__snippet" href="#">useful snippet</a></div>
</div></body></html>`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// --- web_search: httptest サーバを endpoint に指定 ---
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(ddgFixture))
	}))
	defer srv.Close()

	ws := tool.NewWebSearch(config.WebSearchToolConfig{Endpoint: srv.URL, UserAgent: "e2e"})
	res, err := ws.Execute(context.Background(), json.RawMessage(`{"query":"example"}`))
	if err != nil || res.IsError {
		return fmt.Errorf("web_search failed: err=%v content=%s", err, res.Content)
	}
	if !strings.Contains(res.Content, "https://example.com/doc") {
		return fmt.Errorf("web_search missing organic result: %s", res.Content)
	}
	if strings.Contains(res.Content, "ads.example") {
		return fmt.Errorf("web_search must exclude ads: %s", res.Content)
	}
	fmt.Println("search_ok=true")

	// --- web_fetch: スタブ webgrab で本文とページング案内を検証 ---
	dir, err := os.MkdirTemp("", "web-fixture-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	stub := filepath.Join(dir, "webgrab")
	body := `{"markdown":"# Example Doc\nbody text","untrusted":true,"total_chars":9000}`
	script := "#!/bin/sh\ncat <<'BODY'\n" + body + "\nBODY\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		return err
	}

	wf := tool.NewWebFetch(config.WebFetchToolConfig{WebgrabPath: stub, MaxChars: 4000}, nil)
	res, err = wf.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/doc"}`))
	if err != nil || res.IsError {
		return fmt.Errorf("web_fetch failed: err=%v content=%s", err, res.Content)
	}
	if !strings.Contains(res.Content, "# Example Doc") {
		return fmt.Errorf("web_fetch missing markdown: %s", res.Content)
	}
	if !strings.Contains(res.Content, "start_index=4000") {
		return fmt.Errorf("web_fetch missing paging guidance: %s", res.Content)
	}
	fmt.Println("fetch_ok=true")
	return nil
}
