package agent

import (
	"context"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// Input エージェント Run 入力
type Input struct {
	Model        string
	SystemPrompt string
	Messages     []llm.Message
	MaxToolHops  int
	EnabledTools []string
}

// EventKind イベント種別
type EventKind int

// EventKind 定数
const (
	EventDelta EventKind = iota
	EventToolCall
	EventToolResult
	EventFinal
	EventError
)

// ToolResult tool 実行結果
type ToolResult struct {
	CallID  string
	Name    string
	Content string
	IsError bool
}

// Event 1 つのイベント
type Event struct {
	Kind       EventKind
	Delta      string
	ToolCall   *llm.ToolCall
	ToolResult *ToolResult
	Final      *llm.Message
	Err        error
}

// Service エージェント実行サービス
type Service interface {
	Run(ctx context.Context, in Input, out chan<- Event) error
}

type service struct {
	reg   llm.Registry
	tools tool.Registry
}

// New Service を構築する
func New(reg llm.Registry, tools tool.Registry) Service {
	return &service{reg: reg, tools: tools}
}
