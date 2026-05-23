// Package eval ゴールデンデータセットに基づく agent の評価フレームワーク
// tool_recall / tool_precision / param_accuracy / phrase_recall を計算し
// JSON レポートを書き出して回帰検出の基盤を提供する
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// Case 評価ケースの 1 件
type Case struct {
	ID       string       `yaml:"id" json:"id"`
	Input    CaseInput    `yaml:"input" json:"input"`
	Expected CaseExpected `yaml:"expected" json:"expected"`
	Metrics  CaseMetrics  `yaml:"metrics" json:"metrics"`
}

// CaseInput 評価ケースの入力
type CaseInput struct {
	SystemPrompt string        `yaml:"system_prompt" json:"system_prompt"`
	Messages     []CaseMessage `yaml:"messages" json:"messages"`
}

// CaseMessage 評価ケースの会話メッセージ
type CaseMessage struct {
	Role    string `yaml:"role" json:"role"`
	Content string `yaml:"content" json:"content"`
}

// CaseExpected 期待される結果
type CaseExpected struct {
	ToolCalls []ExpectedCall `yaml:"tool_calls" json:"tool_calls"`
	Phrases   []string       `yaml:"phrases" json:"phrases"`
}

// ExpectedCall 期待されるツール呼び出し
type ExpectedCall struct {
	Tool   string         `yaml:"tool" json:"tool"`
	Params map[string]any `yaml:"params" json:"params"`
}

// CaseMetrics 合格判定のしきい値
type CaseMetrics struct {
	ToolRecallMin    float64 `yaml:"tool_recall_min" json:"tool_recall_min"`
	ParamAccuracyMin float64 `yaml:"param_accuracy_min" json:"param_accuracy_min"`
	PhraseRecallMin  float64 `yaml:"phrase_recall_min" json:"phrase_recall_min"`
}

// RunResult 1 ケース実行の生データ
type RunResult struct {
	CaseID    string         `json:"case_id"`
	Source    string         `json:"source"`
	ToolCalls []llm.ToolCall `json:"tool_calls"`
	FinalText string         `json:"final_text"`
}

// Scores 計算済みメトリクス
type Scores struct {
	ToolRecall    float64 `json:"tool_recall"`
	ToolPrecision float64 `json:"tool_precision"`
	ParamAccuracy float64 `json:"param_accuracy"`
	PhraseRecall  float64 `json:"phrase_recall"`
	Passed        bool    `json:"passed"`
}

// Report 全体レポート
type Report struct {
	Cases     []Case      `json:"cases"`
	Results   []RunResult `json:"results"`
	Scores    []Scores    `json:"scores"`
	Aggregate Aggregate   `json:"aggregate"`
}

// Aggregate ケース横断の集計
type Aggregate struct {
	Cases         int     `json:"cases"`
	Passed        int     `json:"passed"`
	ToolRecallAvg float64 `json:"tool_recall_avg"`
	ParamAccAvg   float64 `json:"param_accuracy_avg"`
	PhraseRecAvg  float64 `json:"phrase_recall_avg"`
}

// LoadSuite ディレクトリ内の *.yaml を全てロードする
func LoadSuite(dir string) ([]Case, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("glob suite: %w", err)
	}
	out := make([]Case, 0, len(matches))
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var c Case
		if err := yaml.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// RunSuite agent.Service で suite を実行し RunResult を返す
func RunSuite(ctx context.Context, svc agent.Service, cases []Case, model string, maxHops int) ([]RunResult, error) {
	out := make([]RunResult, 0, len(cases))
	for _, c := range cases {
		msgs := make([]llm.Message, 0, len(c.Input.Messages))
		for _, m := range c.Input.Messages {
			msgs = append(msgs, llm.Message{Role: llm.Role(m.Role), Content: m.Content})
		}
		in := agent.Input{
			Model:        model,
			SystemPrompt: c.Input.SystemPrompt,
			Messages:     msgs,
			MaxToolHops:  maxHops,
			SessionID:    "eval-" + c.ID,
		}
		eventCh := make(chan agent.Event, 32)
		errCh := make(chan error, 1)
		go func() {
			defer close(eventCh)
			errCh <- svc.Run(ctx, in, eventCh)
		}()
		res := RunResult{CaseID: c.ID, Source: "suite"}
		for ev := range eventCh {
			switch ev.Kind {
			case agent.EventToolCall:
				if ev.ToolCall != nil {
					res.ToolCalls = append(res.ToolCalls, *ev.ToolCall)
				}
			case agent.EventFinal:
				if ev.Final != nil {
					res.FinalText = ev.Final.Content
				}
			}
		}
		if err := <-errCh; err != nil {
			// 実行エラーは記録するが集計は継続する
			res.FinalText = "[error] " + err.Error()
		}
		out = append(out, res)
	}
	return out, nil
}

// Score 1 ケースのメトリクスを計算する
func Score(c Case, r RunResult) Scores {
	expectedNames := map[string]bool{}
	for _, ec := range c.Expected.ToolCalls {
		expectedNames[ec.Tool] = true
	}
	actualNames := map[string]bool{}
	for _, ac := range r.ToolCalls {
		actualNames[ac.Name] = true
	}

	var tp int
	for name := range expectedNames {
		if actualNames[name] {
			tp++
		}
	}
	toolRecall := 1.0
	if len(expectedNames) > 0 {
		toolRecall = float64(tp) / float64(len(expectedNames))
	}
	toolPrecision := 1.0
	if len(actualNames) > 0 {
		toolPrecision = float64(tp) / float64(len(actualNames))
	}

	paramAcc := 1.0
	if len(c.Expected.ToolCalls) > 0 {
		var matched int
		for _, ec := range c.Expected.ToolCalls {
			for _, ac := range r.ToolCalls {
				if ac.Name == ec.Tool && paramsMatch(ec.Params, ac.Arguments) {
					matched++
					break
				}
			}
		}
		paramAcc = float64(matched) / float64(len(c.Expected.ToolCalls))
	}

	phraseRecall := 1.0
	if len(c.Expected.Phrases) > 0 {
		var hit int
		for _, p := range c.Expected.Phrases {
			if strings.Contains(r.FinalText, p) {
				hit++
			}
		}
		phraseRecall = float64(hit) / float64(len(c.Expected.Phrases))
	}

	passed := toolRecall >= c.Metrics.ToolRecallMin &&
		paramAcc >= c.Metrics.ParamAccuracyMin &&
		phraseRecall >= c.Metrics.PhraseRecallMin
	return Scores{ToolRecall: toolRecall, ToolPrecision: toolPrecision, ParamAccuracy: paramAcc, PhraseRecall: phraseRecall, Passed: passed}
}

// paramsMatch 期待 params と実引数 JSON が一致するか判定する
func paramsMatch(expected map[string]any, args json.RawMessage) bool {
	if len(expected) == 0 {
		return true
	}
	var actual map[string]any
	if err := json.Unmarshal(args, &actual); err != nil {
		return false
	}
	for k, v := range expected {
		if av, ok := actual[k]; !ok {
			return false
		} else if !equalAny(av, v) {
			return false
		}
	}
	return true
}

// equalAny JSON 値 2 つを比較する
// json.Marshal が失敗した場合は reflect.DeepEqual にフォールバックして
// 空 byte 列の偽陽性一致を防ぐ
func equalAny(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return reflect.DeepEqual(a, b)
	}
	return string(ab) == string(bb)
}

// WriteReport JSON レポートを path に書き出す
func WriteReport(path string, cases []Case, results []RunResult, scores []Scores) error {
	rep := Report{Cases: cases, Results: results, Scores: scores, Aggregate: aggregateScores(scores)}
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir report: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

// aggregateScores スコアの平均と合格件数を集計する
func aggregateScores(scores []Scores) Aggregate {
	if len(scores) == 0 {
		return Aggregate{}
	}
	a := Aggregate{Cases: len(scores)}
	for _, s := range scores {
		if s.Passed {
			a.Passed++
		}
		a.ToolRecallAvg += s.ToolRecall
		a.ParamAccAvg += s.ParamAccuracy
		a.PhraseRecAvg += s.PhraseRecall
	}
	n := float64(len(scores))
	a.ToolRecallAvg /= n
	a.ParamAccAvg /= n
	a.PhraseRecAvg /= n
	return a
}
