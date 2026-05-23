package safety

import (
	"fmt"
	"regexp"
)

// Redactor テキスト中の機微情報を所定の文字列で置換する
type Redactor interface {
	Redact(text string) string
}

// OutputRedactorRule 出力リダクタの 1 ルール
type OutputRedactorRule struct {
	ID          string
	Regex       string
	Replacement string
}

// OutputRedactorConfig 出力リダクタ全体の設定
type OutputRedactorConfig struct {
	Enabled bool
	Rules   []OutputRedactorRule
}

// regexRedactor 正規表現置換ベースの Redactor
type regexRedactor struct {
	rules []redactRule
}

type redactRule struct {
	re          *regexp.Regexp
	replacement string
}

// NewRedactorFromConfig OutputRedactorConfig から Redactor を構築する
// Enabled=false の場合は素通しの noop Redactor を返す
func NewRedactorFromConfig(c OutputRedactorConfig) (Redactor, error) {
	if !c.Enabled {
		return noopRedactor{}, nil
	}
	rules := make([]redactRule, 0, len(c.Rules))
	for _, r := range c.Rules {
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			return nil, fmt.Errorf("safety: invalid redactor regex for id=%q: %w", r.ID, err)
		}
		rules = append(rules, redactRule{re: re, replacement: r.Replacement})
	}
	return &regexRedactor{rules: rules}, nil
}

// Redact 全ルールを順に適用する
func (r *regexRedactor) Redact(text string) string {
	for _, rl := range r.rules {
		text = rl.re.ReplaceAllString(text, rl.replacement)
	}
	return text
}

// noopRedactor 入力をそのまま返す Redactor
type noopRedactor struct{}

// Redact noop 実装
func (noopRedactor) Redact(text string) string { return text }

// chainRedactor 複数の Redactor を順に適用する合成 Redactor
type chainRedactor struct {
	rs []Redactor
}

// ChainRedactor 複数の Redactor を順に適用する Redactor を返す
// nil や noop だけのチェインは noop と等価
func ChainRedactor(rs ...Redactor) Redactor {
	cleaned := make([]Redactor, 0, len(rs))
	for _, r := range rs {
		if r == nil {
			continue
		}
		cleaned = append(cleaned, r)
	}
	if len(cleaned) == 0 {
		return noopRedactor{}
	}
	if len(cleaned) == 1 {
		return cleaned[0]
	}
	return &chainRedactor{rs: cleaned}
}

// Redact 合成 Redactor のすべてを順に適用する
func (c *chainRedactor) Redact(text string) string {
	for _, r := range c.rs {
		text = r.Redact(text)
	}
	return text
}
