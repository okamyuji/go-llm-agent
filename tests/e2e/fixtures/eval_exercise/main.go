// Package main 07 設計書の Scorer 単体を確認するフィクスチャ
// LoadSuite + Score + WriteReport の一連の流れを fake RunResult で実行する
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/okamyuji/go-llm-agent/internal/eval"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func main() {
	dir, err := os.MkdirTemp("", "eval-fixture-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	caseYAML := `id: refund-test
input:
  system_prompt: assistant
  messages:
    - role: user
      content: refund please
expected:
  tool_calls:
    - tool: refund
      params:
        order: A1
  phrases:
    - done
metrics:
  tool_recall_min: 1.0
  param_accuracy_min: 1.0
  phrase_recall_min: 1.0
`
	if err := os.WriteFile(filepath.Join(dir, "case.yaml"), []byte(caseYAML), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(2)
	}

	cases, err := eval.LoadSuite(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load:", err)
		os.Exit(3)
	}
	if len(cases) != 1 {
		fmt.Fprintln(os.Stderr, "expected 1 case, got", len(cases))
		os.Exit(4)
	}

	// fake RunResult を組み立てる
	results := []eval.RunResult{{
		CaseID:    cases[0].ID,
		Source:    "fixture",
		ToolCalls: []llm.ToolCall{{Name: "refund", Arguments: json.RawMessage(`{"order":"A1"}`)}},
		FinalText: "all done",
	}}
	scores := []eval.Scores{eval.Score(cases[0], results[0])}
	if !scores[0].Passed {
		fmt.Fprintln(os.Stderr, "expected pass, got:", scores[0])
		os.Exit(5)
	}

	reportPath := filepath.Join(dir, "report.json")
	if err := eval.WriteReport(reportPath, cases, results, scores); err != nil {
		fmt.Fprintln(os.Stderr, "write report:", err)
		os.Exit(6)
	}

	b, err := os.ReadFile(reportPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read report:", err)
		os.Exit(7)
	}
	var rep eval.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		fmt.Fprintln(os.Stderr, "parse report:", err)
		os.Exit(8)
	}
	fmt.Printf("aggregate_cases=%d aggregate_passed=%d\n", rep.Aggregate.Cases, rep.Aggregate.Passed)
}
