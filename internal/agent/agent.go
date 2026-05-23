package agent

import (
	"context"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/safety"
	"github.com/okamyuji/go-llm-agent/internal/tool"
)

// Input エージェント Run 入力
type Input struct {
	Model                string
	SystemPrompt         string
	Messages             []llm.Message
	MaxToolHops          int
	EnabledTools         []string
	SessionID            string
	ToolChoice           *llm.ToolChoice
	ValidationMaxRetries int
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
	EventUsage
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
	Usage      *llm.Usage
	Cost       *billing.Snapshot
}

// Service エージェント実行サービス
type Service interface {
	Run(ctx context.Context, in Input, out chan<- Event) error
}

type service struct {
	reg               llm.Registry
	tools             tool.Registry
	billing           billing.Accumulator
	validator         SchemaValidator
	defaultToolChoice *llm.ToolChoice
	defaultMaxRetries int
	scanner           safety.Scanner
	redactor          safety.Redactor
	approver          Approver
	approvalRequired  map[string]bool
	approvalTimeout   time.Duration
	strategy          Strategy
}

// New Service を構築する。billing.Accumulator は nil 可で、その場合は集計を無効にする
func New(reg llm.Registry, tools tool.Registry, opts ...Option) Service {
	s := &service{reg: reg, tools: tools}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Option Service コンストラクタの関数オプション
type Option func(*service)

// WithBilling Service に billing.Accumulator を注入する
func WithBilling(acc billing.Accumulator) Option {
	return func(s *service) { s.billing = acc }
}

// WithValidator Service にツール引数 JSON Schema 検証を注入する
func WithValidator(v SchemaValidator) Option {
	return func(s *service) { s.validator = v }
}

// WithDefaultToolChoice Input.ToolChoice が nil のときの既定値を設定する
func WithDefaultToolChoice(tc *llm.ToolChoice) Option {
	return func(s *service) { s.defaultToolChoice = tc }
}

// WithDefaultValidationRetries Input.ValidationMaxRetries が 0 のときの既定値を設定する
func WithDefaultValidationRetries(n int) Option {
	return func(s *service) { s.defaultMaxRetries = n }
}

// WithScanner 入力スキャナを注入する
func WithScanner(sc safety.Scanner) Option {
	return func(s *service) { s.scanner = sc }
}

// WithRedactor 出力リダクタを注入する
func WithRedactor(r safety.Redactor) Option {
	return func(s *service) { s.redactor = r }
}

// WithStrategy 戦略を注入する。未指定の場合は ReAct が既定
func WithStrategy(st Strategy) Option {
	return func(s *service) { s.strategy = st }
}

// WithApprover 承認ハンドラを注入する。required ツールセットも合わせて指定する
func WithApprover(ap Approver, requiredTools []string, timeout time.Duration) Option {
	set := make(map[string]bool, len(requiredTools))
	for _, t := range requiredTools {
		set[t] = true
	}
	return func(s *service) {
		s.approver = ap
		s.approvalRequired = set
		s.approvalTimeout = timeout
	}
}
