package audit

import (
	"encoding/json"
	"time"
)

// Kind イベント種別
type Kind string

// Kind 定数
const (
	KindLLMRequest  Kind = "llm_request"
	KindLLMResponse Kind = "llm_response"
	KindToolCall    Kind = "tool_call"
	KindToolResult  Kind = "tool_result"
	KindUsage       Kind = "usage"
)

// MaxPayloadBytes Iggy 0.8.0 の MAX_PAYLOAD_SIZE と同じ
const MaxPayloadBytes = 64 * 1000 * 1000

// Event 監査イベント 1 件。schema/event.schema.json v1 に対応する
type Event struct {
	V         int             `json:"v"`
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	RunID     string          `json:"run_id"`
	Seq       uint64          `json:"seq"`
	TS        time.Time       `json:"ts"`
	Kind      Kind            `json:"kind"`
	Provider  string          `json:"provider,omitempty"`
	Model     string          `json:"model,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

// MessagePayload llm_request.messages の 1 要素
type MessagePayload struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	Name       string            `json:"name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCallPayload `json:"tool_calls,omitempty"`
}

// ToolCallPayload ツール呼出。tool_call イベントの payload と llm_response.tool_call の両方に使う
type ToolCallPayload struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// LLMRequestPayload llm_request の payload
type LLMRequestPayload struct {
	Messages    []MessagePayload `json:"messages"`
	Tools       []string         `json:"tools,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
}

// LLMResponsePayload llm_response の payload
type LLMResponsePayload struct {
	Content  string           `json:"content,omitempty"`
	ToolCall *ToolCallPayload `json:"tool_call,omitempty"`
	Finish   string           `json:"finish,omitempty"`
	Error    string           `json:"error,omitempty"`
}

// ToolResultPayload tool_result の payload
type ToolResultPayload struct {
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
	DurationMS int64  `json:"duration_ms"`
}

// UsagePayload usage の payload
type UsagePayload struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// TruncatedPayload 64MB 超の payload の置き換え形
type TruncatedPayload struct {
	Truncated bool `json:"truncated"`
	Bytes     int  `json:"bytes"`
}

// Marshal 1 行 JSON にする（末尾改行なし）
func (e Event) Marshal() ([]byte, error) {
	return json.Marshal(e)
}

// Truncated payload を切り詰め形に置き換えた複製を返す
func Truncated(e Event, n int) (Event, error) {
	p, err := json.Marshal(TruncatedPayload{Truncated: true, Bytes: n})
	if err != nil {
		return Event{}, err
	}
	e.Payload = p
	return e, nil
}
