package llm

import "context"

// ChatRequest LLM への問い合わせ
type ChatRequest struct {
	Model       string
	Messages    []Message
	Tools       []ToolSpec
	ToolChoice  *ToolChoice
	Temperature *float64
	MaxTokens   *int
}

// ToolChoice ツール呼び出しの強制度を表す
// Mode は "auto" / "required" / "none" / "tool" のいずれかを取り、
// Mode=="tool" のとき Name に具体的なツール名を指定する
type ToolChoice struct {
	Mode string
	Name string
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
