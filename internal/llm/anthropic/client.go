package anthropic

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

const apiVersion = "2023-06-01"

// Options クライアント生成オプション
// HTTPClient が指定されている場合、RequestTimeoutSeconds は無視される
// (タイムアウト管理は呼び出し側の HTTPClient.Timeout に委ねる設計)
type Options struct {
	BaseURL               string
	APIKey                string
	HTTPClient            *http.Client
	RequestTimeoutSeconds int
}

// Client Anthropic Messages API クライアント
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
		o.BaseURL = "https://api.anthropic.com"
	}
	return &Client{baseURL: o.BaseURL, apiKey: o.APIKey, http: c}
}

// Name プロバイダー名を返す
func (c *Client) Name() string { return "anthropic" }

type msgPayload struct {
	Model      string         `json:"model"`
	MaxTokens  int            `json:"max_tokens"`
	System     string         `json:"system,omitempty"`
	Messages   []msgPayloadIn `json:"messages"`
	Stream     bool           `json:"stream,omitempty"`
	Tools      []toolDecl     `json:"tools,omitempty"`
	ToolChoice any            `json:"tool_choice,omitempty"`
}

type msgPayloadIn struct {
	Role    string            `json:"role"`
	Content []msgPayloadBlock `json:"content"`
}

type msgPayloadBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type toolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type msgResp struct {
	StopReason string            `json:"stop_reason"`
	Content    []msgPayloadBlock `json:"content"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Chat 同期で Anthropic に問い合わせる
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p := toPayload(req, false)
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("anthropic marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)
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
			Underlying: fmt.Errorf("anthropic http %d: %s", res.StatusCode, string(b)),
		}
	}

	var parsed msgResp
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("anthropic decode: %w", err)
	}

	out := &llm.ChatResponse{
		Message:      llm.Message{Role: llm.RoleAssistant},
		Usage:        llm.Usage{InputTokens: parsed.Usage.InputTokens, OutputTokens: parsed.Usage.OutputTokens},
		FinishReason: parsed.StopReason,
	}
	for _, blk := range parsed.Content {
		switch blk.Type {
		case "text":
			out.Message.Content += blk.Text
		case "tool_use":
			out.Message.ToolCalls = append(out.Message.ToolCalls, llm.ToolCall{
				ID:        blk.ID,
				Name:      blk.Name,
				Arguments: blk.Input,
			})
		}
	}
	return out, nil
}

func toPayload(req llm.ChatRequest, stream bool) msgPayload {
	maxTok := 1024
	if req.MaxTokens != nil {
		maxTok = *req.MaxTokens
	}
	p := msgPayload{
		Model:     req.Model,
		MaxTokens: maxTok,
		Stream:    stream,
	}
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			if p.System != "" {
				p.System += "\n"
			}
			p.System += m.Content
			continue
		}
		role := string(m.Role)
		if role == "tool" {
			// Anthropic では tool 結果は user 役の tool_result ブロックで返す
			p.Messages = append(p.Messages, msgPayloadIn{
				Role: "user",
				Content: []msgPayloadBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
				}},
			})
			continue
		}
		blocks := []msgPayloadBlock{}
		if m.Content != "" {
			blocks = append(blocks, msgPayloadBlock{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			blocks = append(blocks, msgPayloadBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: tc.Arguments,
			})
		}
		p.Messages = append(p.Messages, msgPayloadIn{Role: role, Content: blocks})
	}
	for _, t := range req.Tools {
		p.Tools = append(p.Tools, toolDecl{
			Name: t.Name, Description: t.Description, InputSchema: t.Schema,
		})
	}
	if req.ToolChoice != nil {
		p.ToolChoice = toolChoiceJSON(req.ToolChoice)
	}
	return p
}

// toolChoiceJSON ChatRequest.ToolChoice を Anthropic Messages API の値に変換する
// Anthropic は {"type":"auto"} / {"type":"any"} / {"type":"none"} / {"type":"tool","name":"..."}
// の 4 種類を受け付ける。"none" は明示的に type=none を返し、tools 全体の挙動を制御する
// 未知の Mode は auto にフォールバックし、設定ミスを発見しやすくするため警告ログを残す
func toolChoiceJSON(tc *llm.ToolChoice) any {
	switch tc.Mode {
	case "auto", "":
		return map[string]any{"type": "auto"}
	case "required", "any":
		return map[string]any{"type": "any"}
	case "none":
		return map[string]any{"type": "none"}
	case "tool":
		if tc.Name == "" {
			// tool mode は tc.Name 必須。空指定は設定ミスとして警告ログを残し auto にフォールバックする
			slog.Warn("anthropic: tool_choice mode=tool with empty Name, falling back to auto")
			return map[string]any{"type": "auto"}
		}
		return map[string]any{"type": "tool", "name": tc.Name}
	default:
		slog.Warn("anthropic: unknown tool_choice mode, falling back to auto", "mode", tc.Mode)
		return map[string]any{"type": "auto"}
	}
}
