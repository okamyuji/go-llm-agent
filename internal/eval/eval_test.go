package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func TestLoadSuite_ReadsAllYAMLs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c1 := `id: a
input:
  system_prompt: hi
  messages:
    - role: user
      content: hello
expected:
  tool_calls: []
  phrases: []
metrics:
  tool_recall_min: 1.0
`
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(c1), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := LoadSuite(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 || cases[0].ID != "a" {
		t.Fatalf("unexpected cases: %+v", cases)
	}
}

func TestLoadSuite_NoYAMLsIsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases, err := LoadSuite(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 0 {
		t.Errorf("expected 0 cases, got %d", len(cases))
	}
}

func TestLoadSuite_ParseErrorReturnsErr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(dir); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestScore_PerfectMatch(t *testing.T) {
	t.Parallel()
	c := Case{
		ID: "c",
		Expected: CaseExpected{
			ToolCalls: []ExpectedCall{{Tool: "fs_read", Params: map[string]any{"path": "a.txt"}}},
			Phrases:   []string{"done"},
		},
		Metrics: CaseMetrics{ToolRecallMin: 1.0, ParamAccuracyMin: 1.0, PhraseRecallMin: 1.0},
	}
	r := RunResult{
		ToolCalls: []llm.ToolCall{{Name: "fs_read", Arguments: json.RawMessage(`{"path":"a.txt"}`)}},
		FinalText: "all done here",
	}
	s := Score(c, r)
	if !s.Passed {
		t.Fatalf("expected pass, got %+v", s)
	}
	if s.ToolRecall != 1.0 || s.ParamAccuracy != 1.0 || s.PhraseRecall != 1.0 {
		t.Errorf("expected all 1.0, got %+v", s)
	}
}

func TestScore_MissingTool(t *testing.T) {
	t.Parallel()
	c := Case{
		Expected: CaseExpected{
			ToolCalls: []ExpectedCall{{Tool: "fs_read"}, {Tool: "shell"}},
		},
		Metrics: CaseMetrics{ToolRecallMin: 1.0},
	}
	r := RunResult{ToolCalls: []llm.ToolCall{{Name: "fs_read"}}}
	s := Score(c, r)
	if s.ToolRecall != 0.5 {
		t.Errorf("got %v want 0.5", s.ToolRecall)
	}
	if s.Passed {
		t.Error("must not pass when ToolRecallMin not satisfied")
	}
}

func TestScore_NoExpectedMeansFullScore(t *testing.T) {
	t.Parallel()
	c := Case{Metrics: CaseMetrics{}}
	r := RunResult{}
	s := Score(c, r)
	if s.ToolRecall != 1.0 || s.ParamAccuracy != 1.0 || s.PhraseRecall != 1.0 {
		t.Errorf("empty expected must yield all 1.0, got %+v", s)
	}
}

func TestScore_ParamMismatch(t *testing.T) {
	t.Parallel()
	c := Case{
		Expected: CaseExpected{
			ToolCalls: []ExpectedCall{{Tool: "fs_read", Params: map[string]any{"path": "want.txt"}}},
		},
		Metrics: CaseMetrics{ToolRecallMin: 1.0, ParamAccuracyMin: 1.0},
	}
	r := RunResult{ToolCalls: []llm.ToolCall{{Name: "fs_read", Arguments: json.RawMessage(`{"path":"other.txt"}`)}}}
	s := Score(c, r)
	if s.ParamAccuracy != 0 {
		t.Errorf("expected param mismatch, got %v", s.ParamAccuracy)
	}
}

func TestScore_PhraseRecallPartial(t *testing.T) {
	t.Parallel()
	c := Case{
		Expected: CaseExpected{Phrases: []string{"alpha", "beta"}},
		Metrics:  CaseMetrics{PhraseRecallMin: 1.0},
	}
	r := RunResult{FinalText: "only alpha here"}
	s := Score(c, r)
	if s.PhraseRecall != 0.5 {
		t.Errorf("got %v want 0.5", s.PhraseRecall)
	}
}

func TestWriteReport_CreatesValidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	cases := []Case{{ID: "a"}}
	results := []RunResult{{CaseID: "a"}}
	scores := []Scores{{ToolRecall: 1, Passed: true}}
	if err := WriteReport(path, cases, results, scores); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Aggregate.Cases != 1 || r.Aggregate.Passed != 1 {
		t.Errorf("aggregate mismatch: %+v", r.Aggregate)
	}
}

func TestAggregate_EmptyIsZero(t *testing.T) {
	t.Parallel()
	a := aggregateScores(nil)
	if a.Cases != 0 || a.Passed != 0 {
		t.Errorf("empty aggregate must be zero")
	}
}
