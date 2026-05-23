package openai

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
type Options struct {
	BaseURL               string
	APIKey                string
	HTTPClient            *http.Client
	RequestTimeoutSeconds int
}

// Client OpenAI Chat Completions API クライアント
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New Options からクライアントを生成
func New(o Options) *Client {
	c := o.HTTPClient
	if c == nil {
		timeout := 120 * time.Second
		if o.RequestTimeoutSeconds > 0 {
			timeout = time.Duration(o.RequestTimeoutSeconds) * time.Second
		}
		c = &http.Client{Timeout: timeout}
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://api.openai.com/v1"
	}
	return &Client{baseURL: o.BaseURL, apiKey: o.APIKey, http: c}
}

// Name プロバイダー名を返す
func (c *Client) Name() string { return "openai" }

type chatPayload struct {
	Model      string            `json:"model"`
	Messages   []chatPayloadMsg  `json:"messages"`
	Stream     bool              `json:"stream"`
	Tools      []chatPayloadTool `json:"tools,omitempty"`
	ToolChoice any               `json:"tool_choice,omitempty"`
}

type chatPayloadMsg struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	Name       string            `json:"name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []chatPayloadCall `json:"tool_calls,omitempty"`
}

type chatPayloadCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function chatPayloadFunc `json:"function"`
}

type chatPayloadFunc struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type chatPayloadTool struct {
	Type     string          `json:"type"`
	Function chatPayloadFunc `json:"function"`
}

type chatResp struct {
	Choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Role      string            `json:"role"`
			Content   string            `json:"content"`
			ToolCalls []chatPayloadCall `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat 同期で OpenAI に問い合わせる
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	payload := toPayload(req, false)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
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
			Underlying: fmt.Errorf("openai http %d: %s", res.StatusCode, string(b)),
		}
	}

	var parsed chatResp
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("openai decode: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("openai: no choices")
	}
	ch := parsed.Choices[0]
	out := &llm.ChatResponse{
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: ch.Message.Content,
		},
		Usage:        llm.Usage{InputTokens: parsed.Usage.PromptTokens, OutputTokens: parsed.Usage.CompletionTokens},
		FinishReason: ch.FinishReason,
	}
	for _, tc := range ch.Message.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls, llm.ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
		})
	}
	return out, nil
}

func toPayload(req llm.ChatRequest, stream bool) chatPayload {
	p := chatPayload{Model: req.Model, Stream: stream}
	for _, m := range req.Messages {
		pm := chatPayloadMsg{Role: string(m.Role), Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			pm.ToolCalls = append(pm.ToolCalls, chatPayloadCall{
				ID: tc.ID, Type: "function",
				Function: chatPayloadFunc{Name: tc.Name, Arguments: tc.Arguments},
			})
		}
		p.Messages = append(p.Messages, pm)
	}
	for _, t := range req.Tools {
		p.Tools = append(p.Tools, chatPayloadTool{Type: "function", Function: chatPayloadFunc{Name: t.Name, Arguments: t.Schema}})
	}
	if req.ToolChoice != nil {
		p.ToolChoice = toolChoiceJSON(req.ToolChoice)
	}
	return p
}

// toolChoiceJSON ChatRequest.ToolChoice を OpenAI 仕様の値に変換する
// auto/required/none は文字列、tool 指定はオブジェクト形式で返す
// OpenAI の "any" 概念は無く、Anthropic の "any" は実質「必ずツール呼び出し」だが、
// vendor 間混乱を避けるため OpenAI 側では "any" を Auto (model-may-call-tools) に
// マッピングする。強制呼び出しを意図するなら呼び出し側が "required" を指定する
func toolChoiceJSON(tc *llm.ToolChoice) any {
	switch tc.Mode {
	case "auto", "":
		return "auto"
	case "required":
		return "required"
	case "any":
		return "auto"
	case "none":
		return "none"
	case "tool":
		if tc.Name == "" {
			return "auto"
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}
	default:
		// 未知の Mode は "auto" にフォールバックさせるが、設定ミスを発見しやすくするため
		// 警告ログを出す。サイレントフォールバックを避ける
		slog.Warn("openai: unknown tool_choice mode, falling back to auto", "mode", tc.Mode)
		return "auto"
	}
}
