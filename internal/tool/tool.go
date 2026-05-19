package tool

import (
	"context"
	"encoding/json"
)

// Spec ツール宣言。JSON Schema を含む
type Spec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Result ツール実行結果
type Result struct {
	Content   string
	IsError   bool
	Truncated bool
}

// Tool ツール 1 つの抽象
type Tool interface {
	Spec() Spec
	Execute(ctx context.Context, raw json.RawMessage) (Result, error)
}

// Registry ツール一覧と検索
type Registry interface {
	List() []Spec
	Lookup(name string) (Tool, bool)
}
