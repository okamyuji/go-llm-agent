package llm

import "encoding/json"

// Role 会話メッセージの役割
type Role string

// Role 定数
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 1 つの会話メッセージ
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	Name       string
}

// ToolCall LLM からのツール呼び出し依頼
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
	// Metadata プロバイダ固有の往復データ
	// 例 Gemini thinking model の thoughtSignature
	// 受信時にプロバイダが書き込み、次ターンの送信時に同じプロバイダが読み出して payload に詰める
	Metadata map[string]string
}

// ToolSpec ツール定義 (LLM に渡す)
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Usage トークン使用量
type Usage struct {
	InputTokens  int
	OutputTokens int
}
