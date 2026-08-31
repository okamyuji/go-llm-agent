package audit

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func loadSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	s, err := c.Compile("testdata/event.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func baseEvent(kind Kind) Event {
	return Event{V: 1, ID: "1b4e28ba-2fa1-11d2-883f-0016d3cca427", SessionID: "s1",
		RunID: "1b4e28ba-2fa1-11d2-883f-0016d3cca428", Seq: 0, TS: time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC),
		Kind: kind, Provider: "llamacpp", Model: "m"}
}

func validate(t *testing.T, s *jsonschema.Schema, e Event) error {
	t.Helper()
	b, err := e.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return s.Validate(v)
}

func TestEventsMatchSchema(t *testing.T) {
	s := loadSchema(t)
	req := baseEvent(KindLLMRequest)
	req.Payload = mustRaw(t, LLMRequestPayload{Messages: []MessagePayload{{Role: "user", Content: "hi"}}})
	if err := validate(t, s, req); err != nil {
		t.Errorf("llm_request: %v", err)
	}
	res := baseEvent(KindLLMResponse)
	res.CallID = "c1"
	res.Payload = mustRaw(t, LLMResponsePayload{ToolCall: &ToolCallPayload{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{"cmd":"ls"}`)}})
	if err := validate(t, s, res); err != nil {
		t.Errorf("llm_response: %v", err)
	}
	tc := baseEvent(KindToolCall)
	tc.CallID = "c1"
	tc.Payload = mustRaw(t, ToolCallPayload{Name: "shell", Arguments: json.RawMessage(`{"cmd":"ls"}`)})
	if err := validate(t, s, tc); err != nil {
		t.Errorf("tool_call: %v", err)
	}
	tr := baseEvent(KindToolResult)
	tr.CallID = "c1"
	tr.Payload = mustRaw(t, ToolResultPayload{Name: "shell", Content: "ok", IsError: false, DurationMS: 3})
	if err := validate(t, s, tr); err != nil {
		t.Errorf("tool_result: %v", err)
	}
	us := baseEvent(KindUsage)
	us.Payload = mustRaw(t, UsagePayload{InputTokens: 1, OutputTokens: 2})
	if err := validate(t, s, us); err != nil {
		t.Errorf("usage: %v", err)
	}
	trunc, err := Truncated(req, 70_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate(t, s, trunc); err != nil {
		t.Errorf("truncated llm_request: %v", err)
	}
}

func TestEventsRejectedBySchema(t *testing.T) {
	s := loadSchema(t)
	e := baseEvent(KindLLMResponse)
	e.Payload = json.RawMessage(`{}`)
	if err := validate(t, s, e); err == nil {
		t.Error("empty llm_response payload must be rejected")
	}
	e2 := baseEvent(KindLLMRequest)
	e2.SessionID = ""
	e2.Payload = mustRaw(t, LLMRequestPayload{Messages: []MessagePayload{{Role: "user"}}})
	if err := validate(t, s, e2); err == nil {
		t.Error("empty session_id must be rejected")
	}
}

func TestMarshalIsSingleLine(t *testing.T) {
	e := baseEvent(KindUsage)
	e.Payload = mustRaw(t, UsagePayload{})
	b, err := e.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range b {
		if c == '\n' {
			t.Fatal("marshal output must not contain newline")
		}
	}
}
