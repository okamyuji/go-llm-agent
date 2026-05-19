package llm

import "context"

// ChatRequest LLM への問い合わせ
type ChatRequest struct {
	Model       string
	Messages    []Message
	Tools       []ToolSpec
	Temperature *float64
	MaxTokens   *int
}

// ChatResponse 同期応答
type ChatResponse struct {
	Message      Message
	Usage        Usage
	FinishReason string
}

// StreamEvent ストリーム 1 イベント
type StreamEvent struct {
	DeltaText string
	ToolCall  *ToolCall
	Usage     *Usage
	Finish    string
	Err       error
}

// ChatStream ストリームの読み口
type ChatStream interface {
	Recv() (StreamEvent, bool)
	Close() error
}

// Provider LLM プロバイダー実装が満たすインターフェース
type Provider interface {
	Name() string
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	Stream(ctx context.Context, req ChatRequest) (ChatStream, error)
}
