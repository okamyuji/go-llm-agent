package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// Options クライアント生成オプション
// Temperature 非 nil のとき全リクエストの options.temperature に設定する (0 で決定的出力)
// Think 非 nil のとき全リクエストの think に設定する (false で thinking モード無効化)
type Options struct {
	BaseURL               string
	HTTPClient            *http.Client
	RequestTimeoutSeconds int
	Temperature           *float64
	Think                 *bool
}

// Client Ollama API クライアント
type Client struct {
	baseURL     string
	http        *http.Client
	temperature *float64
	think       *bool
}

// maxRequestTimeoutSeconds RequestTimeoutSeconds の上限 (24 時間)
// 設定誤りで巨大値が入ったときの sanity-check として上限を設ける
const maxRequestTimeoutSeconds = 24 * 60 * 60

// New Options からクライアントを生成
func New(o Options) *Client {
	c := o.HTTPClient
	if c == nil {
		timeout := 300 * time.Second
		if o.RequestTimeoutSeconds > 0 {
			sec := min(o.RequestTimeoutSeconds, maxRequestTimeoutSeconds)
			timeout = time.Duration(sec) * time.Second
		}
		c = &http.Client{Timeout: timeout}
	}
	if o.BaseURL == "" {
		o.BaseURL = "http://localhost:11434"
	}
	return &Client{baseURL: o.BaseURL, http: c, temperature: o.Temperature, think: o.Think}
}

// Name プロバイダー名を返す
func (c *Client) Name() string { return "ollama" }

type ollamaMsg struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	Name      string           `json:"name,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaFunc `json:"function"`
}

type ollamaFunc struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ollamaToolDecl struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type ollamaPayload struct {
	Model      string           `json:"model"`
	Messages   []ollamaMsg      `json:"messages"`
	Stream     bool             `json:"stream"`
	Tools      []ollamaToolDecl `json:"tools,omitempty"`
	ToolChoice any              `json:"tool_choice,omitempty"`
	Options    map[string]any   `json:"options,omitempty"`
	Think      *bool            `json:"think,omitempty"`
}

type ollamaResp struct {
	Message struct {
		Role      string           `json:"role"`
		Content   string           `json:"content"`
		ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

// Chat 同期で Ollama に問い合わせる
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p := c.toPayload(req, false)
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("ollama marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	res, err := c.http.Do(httpReq)
	if err != nil {
		return nil, &llm.ProviderError{Provider: c.Name(), Retryable: true, Underlying: err}
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		return nil, &llm.ProviderError{
			Provider:   c.Name(),
			StatusCode: res.StatusCode,
			Retryable:  res.StatusCode == 429 || res.StatusCode >= 500,
			Underlying: fmt.Errorf("ollama http %d: %s", res.StatusCode, string(b)),
		}
	}

	var parsed ollamaResp
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("ollama decode: %w", err)
	}
	out := &llm.ChatResponse{
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: parsed.Message.Content,
		},
		Usage:        llm.Usage{InputTokens: parsed.PromptEvalCount, OutputTokens: parsed.EvalCount},
		FinishReason: parsed.DoneReason,
	}
	for i, tc := range parsed.Message.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls, llm.ToolCall{
			ID:        fmt.Sprintf("call_%d", i+1),
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return out, nil
}

func (c *Client) toPayload(req llm.ChatRequest, stream bool) ollamaPayload {
	p := ollamaPayload{Model: req.Model, Stream: stream}
	if c.temperature != nil {
		p.Options = map[string]any{"temperature": *c.temperature}
	}
	if c.think != nil {
		p.Think = c.think
	}
	for _, m := range req.Messages {
		om := ollamaMsg{Role: string(m.Role), Content: m.Content, Name: m.Name}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, ollamaToolCall{Function: ollamaFunc{Name: tc.Name, Arguments: tc.Arguments}})
		}
		p.Messages = append(p.Messages, om)
	}
	for _, t := range req.Tools {
		td := ollamaToolDecl{Type: "function"}
		td.Function.Name = t.Name
		td.Function.Description = t.Description
		td.Function.Parameters = t.Schema
		p.Tools = append(p.Tools, td)
	}
	if req.ToolChoice != nil {
		p.ToolChoice = toolChoiceJSON(req.ToolChoice)
	}
	return p
}

// toolChoiceJSON ChatRequest.ToolChoice を Ollama の OpenAI 互換値に変換する
// Ollama 0.4 系では tool_choice をサポートする model がある
// OpenAI 互換の有効値は "auto" / "none" / function 指定オブジェクトのみで、"required" は受け付けない
// "required" / "any" / "tool" のいずれも、特定 tool 名が指定されていれば function 指定として送り、
// 未指定なら "auto" にフォールバックする
// 未知の Mode は "auto" にフォールバックし、設定ミス発見のため警告ログを残す
func toolChoiceJSON(tc *llm.ToolChoice) any {
	switch tc.Mode {
	case "auto", "":
		return "auto"
	case "none":
		return "none"
	case "tool", "required", "any":
		if tc.Name != "" {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": tc.Name},
			}
		}
		// Ollama は OpenAI 互換ながら "required" 文字列を受け付けないため、
		// tc.Name 未指定の "required" は強制呼び出しを実現できず auto にフォールバックする
		// 設定ミス発見のため警告ログを残す
		slog.Warn("ollama: tool_choice with empty Name cannot enforce required, falling back to auto",
			"mode", tc.Mode, "name", tc.Name)
		return "auto"
	default:
		slog.Warn("ollama: unknown tool_choice mode, falling back to auto", "mode", tc.Mode)
		return "auto"
	}
}
