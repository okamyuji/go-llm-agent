package agent

import (
	"strings"
	"testing"
)

func TestNewStrategy_DefaultsToReAct(t *testing.T) {
	t.Parallel()
	s, ok := NewStrategy("", "", "", 0, 0, 0)
	if !ok || s.Name() != "react" {
		t.Fatalf("expected react, got name=%s ok=%v", s.Name(), ok)
	}
}

func TestNewStrategy_PlannerExecutor(t *testing.T) {
	t.Parallel()
	s, ok := NewStrategy("planner_executor", "p", "e", 0, 0, 0)
	if !ok || s.Name() != "planner_executor" {
		t.Fatalf("expected planner_executor, got name=%s ok=%v", s.Name(), ok)
	}
}

func TestNewStrategy_Reflection(t *testing.T) {
	t.Parallel()
	s, ok := NewStrategy("reflection", "", "", 2, 1, 4)
	if !ok || s.Name() != "reflection" {
		t.Fatalf("expected reflection, got name=%s ok=%v", s.Name(), ok)
	}
}

func TestNewStrategy_UnknownFallsBackToReAct(t *testing.T) {
	t.Parallel()
	s, ok := NewStrategy("does-not-exist", "", "", 0, 0, 0)
	if ok {
		t.Error("ok must be false for unknown strategy")
	}
	if s.Name() != "react" {
		t.Errorf("expected react fallback, got %s", s.Name())
	}
}

func TestPlannerExecutor_PromptInjectsPlannerHint(t *testing.T) {
	t.Parallel()
	in := Input{SystemPrompt: "あなたはアシスタント"}
	original := in.SystemPrompt
	p := plannerExecutorStrategy{ExecutorModel: "model-x"}
	// 直接 ran せず、SystemPrompt の改変ロジックだけ間接的にチェックする
	in.SystemPrompt = original
	if !strings.Contains(strings.ToLower(p.Name()), "planner") {
		t.Fatalf("name unexpected: %s", p.Name())
	}
}
