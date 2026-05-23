package safety

import (
	"fmt"
	"regexp"
)

// PIIRule PIIRedactor の 1 ルール
type PIIRule struct {
	ID          string
	Regex       string
	Replacement string
}

// PIIRedactorConfig PII リダクタ全体の設定
type PIIRedactorConfig struct {
	Enabled bool
	Rules   []PIIRule
}

// PIIRedactor 06 番設計書の Redactor インターフェースを実装する PII 専用 Redactor
// agent.Service は ChainRedactor(outputRedactor, piiRedactor) で 1 つの Redactor として扱う
// 内部の redactRule 型は同じ safety パッケージの redactor.go で定義済み
type PIIRedactor struct {
	rules []redactRule
}

// NewPIIRedactor 設定から PIIRedactor を構築する
// Enabled=false の場合は素通しの noop に等価な PIIRedactor を返す
func NewPIIRedactor(c PIIRedactorConfig) (*PIIRedactor, error) {
	if !c.Enabled {
		return &PIIRedactor{}, nil
	}
	rules := make([]redactRule, 0, len(c.Rules))
	for _, r := range c.Rules {
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			return nil, fmt.Errorf("safety: invalid pii regex id=%q: %w", r.ID, err)
		}
		rules = append(rules, redactRule{re: re, replacement: r.Replacement})
	}
	return &PIIRedactor{rules: rules}, nil
}

// Redact 06 番の Redactor インターフェースを満たすメソッド
func (r *PIIRedactor) Redact(text string) string {
	for _, rl := range r.rules {
		text = rl.re.ReplaceAllString(text, rl.replacement)
	}
	return text
}
