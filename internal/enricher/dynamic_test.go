package enricher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTMLToText_Sections(t *testing.T) {
	html := `<html><head><style>.x{}</style><script>var a=1;</script></head>
<body>
<h2>Defer statements</h2>
<p>A defer statement invokes a function later.</p>
<h2>Channel types</h2>
<p>A channel provides communication.</p>
</body></html>`

	text := htmlToText(html)
	if strings.Contains(text, "var a=1") {
		t.Error("script content should be removed")
	}
	secs := splitSections(text)
	if len(secs) != 2 {
		t.Fatalf("expected 2 sections, got %d: %+v", len(secs), secs)
	}
	if secs[0].title != "Defer statements" {
		t.Errorf("title got %q", secs[0].title)
	}
	if !strings.Contains(secs[1].body, "channel provides") {
		t.Errorf("body got %q", secs[1].body)
	}
}

func TestHTMLToText_TableCells(t *testing.T) {
	html := `<html><body>
<h2>挙動の違い</h2>
<table>
<tr><th>構文</th><th>return</th></tr>
<tr><td>Proc.new</td><td>メソッドを抜ける</td></tr>
<tr><td>lambda</td><td>手続きオブジェクトを抜ける</td></tr>
</table>
</body></html>`

	text := htmlToText(html)
	if !strings.Contains(text, "Proc.new | メソッドを抜ける") {
		t.Errorf("table cells should keep separator, got: %s", text)
	}
	if !strings.Contains(text, "lambda | 手続きオブジェクトを抜ける") {
		t.Errorf("lambda row should keep separator, got: %s", text)
	}
}

func TestTrimAtBoundary(t *testing.T) {
	t.Run("under limit unchanged", func(t *testing.T) {
		if got := trimAtBoundary("short", 100); got != "short" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("cuts at newline boundary", func(t *testing.T) {
		s := strings.Repeat("a", 50) + "\n" + strings.Repeat("b", 50) + "\n" + strings.Repeat("c", 50)
		got := trimAtBoundary(s, 120)
		if strings.Contains(got, "c") {
			t.Errorf("should cut before c-line, got: %q", got)
		}
		if !strings.HasSuffix(got, strings.Repeat("b", 50)) {
			t.Errorf("should keep complete b-line, got: %q", got)
		}
	})
	t.Run("no newline cuts at rune boundary", func(t *testing.T) {
		s := strings.Repeat("あ", 100)
		got := trimAtBoundary(s, 100)
		if !utf8ValidString(got) {
			t.Errorf("result should be valid UTF-8: %q", got)
		}
	})
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestExtractTerms(t *testing.T) {
	q := "以下のGoコードの出力を答えてください。defer fmt.Println(i) と sync.WaitGroup の挙動"
	terms := extractTerms(q)

	want := map[string]bool{"defer": false, "fmt.println": false, "sync.waitgroup": false}
	for _, term := range terms {
		if _, ok := want[term]; ok {
			want[term] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("expected term %q not extracted from %v", k, terms)
		}
	}
}

func TestScoreSection_TitleWeighted(t *testing.T) {
	secTitle := section{title: "Defer statements", body: "something else"}
	secBody := section{title: "Other", body: "defer is mentioned here once"}
	terms := []string{"defer"}

	if scoreSection(secTitle, terms) <= scoreSection(secBody, terms) {
		t.Error("title match should outweigh single body match")
	}
}

func TestRetrieve_SelectsRelevantSection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>
<h2>Variables</h2>
<p>A variable is a storage location.</p>
<h2>Defer statements</h2>
<p>Each defer statement pushes a function call onto a list. The list is executed in LIFO order when the surrounding function returns. defer defer defer.</p>
<h2>Constants</h2>
<p>Constants may be typed or untyped.</p>
</body></html>`))
	}))
	defer srv.Close()

	r := newRetriever(DynamicConfig{
		Enabled:  true,
		CacheDir: t.TempDir(),
		Sources:  map[string][]string{"go": {srv.URL}},
	})

	got := r.retrieve(context.Background(), []string{"go"}, "Goのdeferの実行順序を教えてください")
	if got == "" {
		t.Fatal("expected non-empty retrieval")
	}
	if !strings.Contains(got, "Defer statements") {
		t.Errorf("expected defer section selected, got: %s", got)
	}
}

func TestRetrieve_UsesCache(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`<html><body><h2>Defer</h2><p>defer runs last. defer.</p></body></html>`))
	}))
	defer srv.Close()

	r := newRetriever(DynamicConfig{
		Enabled:  true,
		CacheDir: t.TempDir(),
		Sources:  map[string][]string{"go": {srv.URL}},
	})

	ctx := context.Background()
	_ = r.retrieve(ctx, []string{"go"}, "defer の挙動")
	_ = r.retrieve(ctx, []string{"go"}, "defer の挙動")
	if calls != 1 {
		t.Errorf("expected 1 HTTP fetch with cache, got %d", calls)
	}
}

func TestRetrieve_NoMatchReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h2>Constants</h2><p>Constants may be typed.</p></body></html>`))
	}))
	defer srv.Close()

	r := newRetriever(DynamicConfig{
		Enabled:  true,
		CacheDir: t.TempDir(),
		Sources:  map[string][]string{"go": {srv.URL}},
	})

	got := r.retrieve(context.Background(), []string{"go"}, "未知トピック zzzqqq について")
	if got != "" {
		t.Errorf("expected empty retrieval for no match, got: %s", got)
	}
}
