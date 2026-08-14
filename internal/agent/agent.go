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
	// TurnMessages は1回のRunで入力履歴の後へ追加したメッセージ列。
	// 対話クライアントがtool callとtool結果を次ターンへ引き継ぐために使う。
	TurnMessages []llm.Message
	Err          error
	Usage        *llm.Usage
	Cost         *billing.Snapshot
}

// Service エージェント実行サービス
type Service interface {
	Run(ctx context.Context, in Input, out chan<- Event) error
}

// ContextEnricher LLM 呼び出し前にメッセージを拡充する関数。
// システムプロンプト挿入後、安全スキャナ前に呼ばれる。
// ユーザーメッセージを解析し、関連する言語仕様などの追加コンテキストを注入できる
type ContextEnricher func(ctx context.Context, messages []llm.Message) ([]llm.Message, error)

type service struct {
	reg                     llm.Registry
	tools                   tool.Registry
	billing                 billing.Accumulator
	validator               SchemaValidator
	defaultToolChoice       *llm.ToolChoice
	defaultMaxRetries       int
	scanner                 safety.Scanner
	redactor                safety.Redactor
	decider                 ApprovalDecider
	approvalRequired        map[string]bool
	approvalTimeout         time.Duration
	strategy                Strategy
	enricher                ContextEnricher
	toolResultLimitMaxChars int
	hooks                   *HookRunner
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

// WithContextEnricher LLM 呼び出し前にメッセージを拡充する関数を注入する
func WithContextEnricher(e ContextEnricher) Option {
	return func(s *service) { s.enricher = e }
}

// WithToolResultLimit ツール結果を履歴へ積む際の上限文字数 (rune 数) を設定する。
// 0 以下を指定すると切り詰めを無効化する。config 側の実効既定値は
// 00-overview 3.4 節が凍結しており、applyDefaults が適用する
func WithToolResultLimit(maxChars int) Option {
	return func(s *service) { s.toolResultLimitMaxChars = maxChars }
}

// WithHooks pre/post ツール実行フックを注入する。hr が nil の場合は既存動作 (フック無効)
func WithHooks(hr *HookRunner) Option {
	return func(s *service) { s.hooks = hr }
}

// WithApprovalDecider 承認判定を注入する。required ツールセットと timeout を合わせて指定する
func WithApprovalDecider(d ApprovalDecider, requiredTools []string, timeout time.Duration) Option {
	set := make(map[string]bool, len(requiredTools))
	for _, t := range requiredTools {
		set[t] = true
	}
	return func(s *service) {
		s.decider = d
		s.approvalRequired = set
		s.approvalTimeout = timeout
	}
}
