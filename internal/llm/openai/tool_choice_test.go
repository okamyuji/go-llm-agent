package openai

import (
	"encoding/json"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

func TestToolChoiceJSON_AutoModeMapsToAuto(t *testing.T) {
	t.Parallel()
	got := toolChoiceJSON(&llm.ToolChoice{Mode: "auto"})
	if got != "auto" {
		t.Errorf("got %v want auto", got)
	}
}

func TestToolChoiceJSON_RequiredMode(t *testing.T) {
	t.Parallel()
	if got := toolChoiceJSON(&llm.ToolChoice{Mode: "required"}); got != "required" {
		t.Errorf("got %v want required", got)
	}
	// llm.ToolChoice.Mode "any" は強制呼び出しを意味するため、OpenAI 側でも "required" に
	// マップして Anthropic/Gemini 側の意味と一致させる
	if got := toolChoiceJSON(&llm.ToolChoice{Mode: "any"}); got != "required" {
		t.Errorf("any must map to required for OpenAI, got %v", got)
	}
}

func TestToolChoiceJSON_NoneMode(t *testing.T) {
	t.Parallel()
	if got := toolChoiceJSON(&llm.ToolChoice{Mode: "none"}); got != "none" {
		t.Errorf("got %v want none", got)
	}
}

func TestToolChoiceJSON_ToolWithName(t *testing.T) {
	t.Parallel()
	got := toolChoiceJSON(&llm.ToolChoice{Mode: "tool", Name: "fs_read"})
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if string(b) != `{"function":{"name":"fs_read"},"type":"function"}` {
		t.Errorf("unexpected JSON: %s", string(b))
	}
}

func TestToolChoiceJSON_ToolWithoutNameFallsBackToAuto(t *testing.T) {
	t.Parallel()
	if got := toolChoiceJSON(&llm.ToolChoice{Mode: "tool"}); got != "auto" {
		t.Errorf("tool without name must default to auto, got %v", got)
	}
}
