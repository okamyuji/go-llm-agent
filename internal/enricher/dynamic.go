package enricher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// DynamicConfig 動的ドキュメント検索の設定
type DynamicConfig struct {
	Enabled       bool                `yaml:"enabled"`
	MaxSections   int                 `yaml:"max_sections"`
	MaxBytes      int                 `yaml:"max_bytes"`
	CacheDir      string              `yaml:"cache_dir"`
	CacheTTLHours int                 `yaml:"cache_ttl_hours"`
	Sources       map[string][]string `yaml:"sources"`
}

const (
	defaultMaxSections   = 5
	defaultMaxBytes      = 6000
	defaultCacheTTLHours = 24
	maxFetchBodyBytes    = 4 << 20
	maxSectionBodyBytes  = 2500
	maxTermOccurrences   = 5
	titleMatchWeight     = 3
	fetchTimeoutSeconds  = 20
)

// retriever 公式ドキュメントから質問に関連するセクションを検索する
type retriever struct {
	http        *http.Client
	cacheDir    string
	ttl         time.Duration
	maxSections int
	maxBytes    int
	sources     map[string][]string
}

func newRetriever(cfg DynamicConfig) *retriever {
	maxSections := cfg.MaxSections
	if maxSections <= 0 {
		maxSections = defaultMaxSections
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	ttlHours := cfg.CacheTTLHours
	if ttlHours <= 0 {
		ttlHours = defaultCacheTTLHours
	}
	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		if base, err := os.UserCacheDir(); err == nil {
			cacheDir = filepath.Join(base, "go-llm-agent", "docs")
		}
	} else if strings.HasPrefix(cacheDir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			cacheDir = filepath.Join(home, cacheDir[2:])
		}
	}
	return &retriever{
		http:        &http.Client{Timeout: fetchTimeoutSeconds * time.Second},
		cacheDir:    cacheDir,
		ttl:         time.Duration(ttlHours) * time.Hour,
		maxSections: maxSections,
		maxBytes:    maxBytes,
		sources:     cfg.Sources,
	}
}

// section ドキュメントの1セクション
type section struct {
	title  string
	body   string
	source string
	score  int
}

// retrieve 検出された言語のドキュメントから質問に関連するセクションを取得する。
// 一致がなければ空文字を返す
func (r *retriever) retrieve(ctx context.Context, langs []string, query string) string {
	terms := extractTerms(query)
	if len(terms) == 0 {
		return ""
	}
	var all []section
	for _, lang := range langs {
		for _, u := range r.sources[strings.ToLower(lang)] {
			text, err := r.fetchText(ctx, u)
			if err != nil {
				slog.WarnContext(ctx, "enricher: doc fetch failed", "url", u, "err", err)
				continue
			}
			for _, sec := range splitSections(text) {
				sec.source = u
				all = append(all, sec)
			}
		}
	}
	if len(all) == 0 {
		return ""
	}

	for i := range all {
		all[i].score = scoreSection(all[i], terms)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })

	var b strings.Builder
	count := 0
	for _, sec := range all {
		if sec.score <= 0 || count >= r.maxSections {
			break
		}
		entry := fmt.Sprintf("### %s\n(出典: %s)\n%s\n\n", sec.title, sec.source, sec.body)
		if b.Len()+len(entry) > r.maxBytes {
			break
		}
		b.WriteString(entry)
		count++
	}
	if count == 0 {
		return ""
	}
	slog.InfoContext(ctx, "enricher: dynamic retrieval", "sections", count, "terms", len(terms), "bytes", b.Len())
	return strings.TrimSpace(b.String())
}

// fetchText URL のテキストをディスクキャッシュ付きで取得する
func (r *retriever) fetchText(ctx context.Context, url string) (string, error) {
	cachePath := ""
	if r.cacheDir != "" {
		sum := sha256.Sum256([]byte(url))
		cachePath = filepath.Join(r.cacheDir, hex.EncodeToString(sum[:])+".txt")
		if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < r.ttl {
			if b, err := os.ReadFile(cachePath); err == nil {
				return string(b), nil
			}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "go-llm-agent/1.0 (+doc-enricher)")
	res, err := r.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("http %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxFetchBodyBytes))
	if err != nil {
		return "", err
	}
	text := htmlToText(string(raw))

	if cachePath != "" {
		if err := os.MkdirAll(r.cacheDir, 0o755); err == nil {
			if werr := os.WriteFile(cachePath, []byte(text), 0o644); werr != nil {
				slog.Warn("enricher: cache write failed", "path", cachePath, "err", werr)
			}
		}
	}
	return text, nil
}

var (
	reScript  = regexp.MustCompile(`(?is)<script.*?</script>`)
	reStyle   = regexp.MustCompile(`(?is)<style.*?</style>`)
	reHeadTag = regexp.MustCompile(`(?is)<h[1-6][^>]*>`)
	reHeadEnd = regexp.MustCompile(`(?is)</h[1-6]>`)
	reCellEnd = regexp.MustCompile(`(?is)</(td|th)>`)
	reBlock   = regexp.MustCompile(`(?is)</?(p|br|li|ul|ol|div|tr|table|pre|blockquote|dd|dt|dl|section|article|code)[^>]*>`)
	reTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlank   = regexp.MustCompile(`\n{3,}`)
)

// htmlToText HTML をプレーンテキストに変換する。見出しは "§ " マーカー付き行になる。
// テーブルセルは " | " 区切りで保持し、挙動比較表などの構造を失わないようにする
func htmlToText(s string) string {
	s = reScript.ReplaceAllString(s, "")
	s = reStyle.ReplaceAllString(s, "")
	s = reHeadTag.ReplaceAllString(s, "\n\n§ ")
	s = reHeadEnd.ReplaceAllString(s, "\n")
	s = reCellEnd.ReplaceAllString(s, " | ")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// splitSections "§ " マーカー行でテキストをセクションに分割する
func splitSections(text string) []section {
	lines := strings.Split(text, "\n")
	var secs []section
	var cur *section
	flush := func() {
		if cur != nil && strings.TrimSpace(cur.body) != "" {
			cur.body = trimAtBoundary(strings.TrimSpace(cur.body), maxSectionBodyBytes)
			secs = append(secs, *cur)
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "§ ") {
			flush()
			cur = &section{title: strings.TrimSpace(strings.TrimPrefix(line, "§ "))}
			continue
		}
		if cur == nil {
			cur = &section{title: "(冒頭)"}
		}
		cur.body += line + "\n"
	}
	flush()
	return secs
}

// trimAtBoundary 上限を超えるテキストを段落 (改行) 境界で切り詰める。
// バイト位置での機械的な切断は表や結論部分を文の途中で壊すため、
// 上限以下で最後に現れる改行位置まで含める。改行がなければ rune 境界で切る
func trimAtBoundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := strings.LastIndex(s[:limit], "\n")
	if cut <= 0 {
		// 改行が見つからない場合は rune 境界に丸めて切る
		cut = limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
	}
	return strings.TrimSpace(s[:cut])
}

var reTerm = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*`)

// termStopwords ドキュメント中で頻出しすぎてシグナルにならない語
var termStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true,
	"this": true, "that": true, "code": true, "main": true,
}

// extractTerms 質問文から ASCII 識別子トークンを抽出する。
// 日本語の質問でもコード片の識別子 (Proc.new, defer 等) が抽出される
func extractTerms(query string) []string {
	seen := make(map[string]bool)
	var terms []string
	for _, m := range reTerm.FindAllString(query, -1) {
		t := strings.ToLower(m)
		if len(t) < 2 || termStopwords[t] || seen[t] {
			continue
		}
		seen[t] = true
		terms = append(terms, t)
	}
	return terms
}

// scoreSection セクションと検索語のマッチ度を計算する。
// タイトル一致は本文一致より重み付けされ、出現回数は上限付き
func scoreSection(sec section, terms []string) int {
	titleLower := strings.ToLower(sec.title)
	bodyLower := strings.ToLower(sec.body)
	score := 0
	for _, t := range terms {
		titleHits := strings.Count(titleLower, t)
		bodyHits := min(strings.Count(bodyLower, t), maxTermOccurrences)
		score += (titleHits*titleMatchWeight + bodyHits) * len(t)
	}
	return score
}
