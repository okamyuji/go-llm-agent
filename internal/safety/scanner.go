// Package safety 入出力テキストの安全性検査と機密マスキングを提供する
package safety

import (
	"fmt"
	"regexp"
)

// ScanFinding Scanner が検出した 1 件の所見
type ScanFinding struct {
	PatternID string
	Snippet   string
}

// Scanner テキストを検査する Scanner インターフェース
type Scanner interface {
	Scan(text string) []ScanFinding
}

// InputScannerRule 入力スキャナ 1 ルールの設定
type InputScannerRule struct {
	ID    string
	Regex string
}

// InputScannerConfig 入力スキャナ全体の設定
type InputScannerConfig struct {
	Enabled      bool
	BlockOnMatch bool
	Patterns     []InputScannerRule
}

// regexScanner 正規表現ベースの Scanner
type regexScanner struct {
	rules []compiledRule
}

type compiledRule struct {
	id string
	re *regexp.Regexp
}

// NewScannerFromConfig InputScannerConfig から Scanner を構築する
// 無効な正規表現はエラーで返し、Enabled=false の場合は空 Scanner を返す
func NewScannerFromConfig(c InputScannerConfig) (Scanner, error) {
	if !c.Enabled {
		return &regexScanner{}, nil
	}
	rules := make([]compiledRule, 0, len(c.Patterns))
	for _, p := range c.Patterns {
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			return nil, fmt.Errorf("safety: invalid scanner regex for id=%q: %w", p.ID, err)
		}
		rules = append(rules, compiledRule{id: p.ID, re: re})
	}
	return &regexScanner{rules: rules}, nil
}

// maxSnippetLen Snippet に格納する文字列の最大長
// ログや HTTP 応答経由で機微な文字列が外部に漏れるリスクを下げるため切り詰める
const maxSnippetLen = 64

// Scan text にマッチしたルール ID と最初のスニペットを返す
// Snippet は maxSnippetLen を超える場合に "..." を付けて切り詰める
func (s *regexScanner) Scan(text string) []ScanFinding {
	if len(s.rules) == 0 || text == "" {
		return nil
	}
	var out []ScanFinding
	for _, r := range s.rules {
		if m := r.re.FindString(text); m != "" {
			if len(m) > maxSnippetLen {
				m = m[:maxSnippetLen] + "..."
			}
			out = append(out, ScanFinding{PatternID: r.id, Snippet: m})
		}
	}
	return out
}
