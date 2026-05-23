package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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

// Client Google Gemini API クライアント
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
		o.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	return &Client{baseURL: o.BaseURL, apiKey: o.APIKey, http: c}
}

// Name プロバイダー名を返す
func (c *Client) Name() string { return "gemini" }

type gemContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []gemPart `json:"parts"`
}

type gemPart struct {
	Text             string           `json:"text,omitempty"`
	FunctionCall     *gemFunctionCall `json:"functionCall,omitempty"`
	FunctionResponse *gemFunctionResp `json:"functionResponse,omitempty"`
	// ThoughtSignature Gemini thinking model が functionCall を含む part に付与する
	// 不透明トークン。次ターンの API リクエストに同じ値を含めないと
	// 400 (INVALID_ARGUMENT) で失敗する
	// 詳細は https://ai.google.dev/gemini-api/docs/thought-signatures
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

type gemFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// metaKeyThoughtSignature llm.ToolCall.Metadata の thoughtSignature 用キー
const metaKeyThoughtSignature = "thoughtSignature"

type gemFunctionResp struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gemPayload struct {
	SystemInstruction *gemContent     `json:"systemInstruction,omitempty"`
	Contents          []gemContent    `json:"contents"`
	Tools             []gemToolsBlock `json:"tools,omitempty"`
	ToolConfig        *gemToolConfig  `json:"toolConfig,omitempty"`
}

type gemToolConfig struct {
	FunctionCallingConfig gemFCCConfig `json:"functionCallingConfig"`
}

type gemFCCConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type gemToolsBlock struct {
	FunctionDeclarations []gemFunctionDecl `json:"functionDeclarations"`
}

type gemFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type gemResponse struct {
	Candidates []struct {
		Content      gemContent `json:"content"`
		FinishReason string     `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

// Chat 同期で Gemini に問い合わせる
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	p := toPayload(req)
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("gemini marshal: %w", err)
	}
	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s", c.baseURL, url.PathEscape(req.Model), url.QueryEscape(c.apiKey))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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
			Underlying: fmt.Errorf("gemini http %d: %s", res.StatusCode, string(b)),
		}
	}

	var parsed gemResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("gemini decode: %w", err)
	}
	if len(parsed.Candidates) == 0 {
		return nil, fmt.Errorf("gemini: no candidates")
	}
	cand := parsed.Candidates[0]
	out := &llm.ChatResponse{
		Message:      llm.Message{Role: llm.RoleAssistant},
		Usage:        llm.Usage{InputTokens: parsed.UsageMetadata.PromptTokenCount, OutputTokens: parsed.UsageMetadata.CandidatesTokenCount},
		FinishReason: cand.FinishReason,
	}
	idx := 0
	for _, part := range cand.Content.Parts {
		if part.Text != "" {
			out.Message.Content += part.Text
		}
		if part.FunctionCall != nil {
			idx++
			tc := llm.ToolCall{
				ID:        fmt.Sprintf("call_%d", idx),
				Name:      part.FunctionCall.Name,
				Arguments: part.FunctionCall.Args,
			}
			if part.ThoughtSignature != "" {
				tc.Metadata = map[string]string{metaKeyThoughtSignature: part.ThoughtSignature}
			}
			out.Message.ToolCalls = append(out.Message.ToolCalls, tc)
		}
	}
	return out, nil
}

func toPayload(req llm.ChatRequest) gemPayload {
	p := gemPayload{}
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem {
			if p.SystemInstruction == nil {
				p.SystemInstruction = &gemContent{Parts: []gemPart{{Text: m.Content}}}
			} else {
				p.SystemInstruction.Parts = append(p.SystemInstruction.Parts, gemPart{Text: m.Content})
			}
			continue
		}
		role := "user"
		if m.Role == llm.RoleAssistant {
			role = "model"
		}
		if m.Role == llm.RoleTool {
			p.Contents = append(p.Contents, gemContent{
				Role: "function",
				Parts: []gemPart{{FunctionResponse: &gemFunctionResp{
					Name:     m.Name,
					Response: json.RawMessage(fmt.Sprintf(`{"content":%q}`, m.Content)),
				}}},
			})
			continue
		}
		parts := []gemPart{}
		if m.Content != "" {
			parts = append(parts, gemPart{Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			gp := gemPart{FunctionCall: &gemFunctionCall{Name: tc.Name, Args: tc.Arguments}}
			if sig, ok := tc.Metadata[metaKeyThoughtSignature]; ok {
				gp.ThoughtSignature = sig
			}
			parts = append(parts, gp)
		}
		p.Contents = append(p.Contents, gemContent{Role: role, Parts: parts})
	}
	if len(req.Tools) > 0 {
		decls := make([]gemFunctionDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, gemFunctionDecl{Name: t.Name, Description: t.Description, Parameters: t.Schema})
		}
		p.Tools = []gemToolsBlock{{FunctionDeclarations: decls}}
	}
	if tc := req.ToolChoice; tc != nil {
		p.ToolConfig = toolChoiceConfig(tc)
	}
	return p
}

// toolChoiceConfig ChatRequest.ToolChoice を Gemini の functionCallingConfig に変換する
// Gemini は AUTO / ANY / NONE と allowedFunctionNames を使う
// 未知の Mode は AUTO にフォールバックし、設定ミスを発見しやすくするため警告ログを残す
// "required" と "any" は OpenAI / Anthropic でも「ツール呼び出し強制」相当として揃えるため
// Gemini でもどちらも ANY にマップする
func toolChoiceConfig(tc *llm.ToolChoice) *gemToolConfig {
	switch tc.Mode {
	case "auto", "":
		return &gemToolConfig{FunctionCallingConfig: gemFCCConfig{Mode: "AUTO"}}
	case "required", "any":
		// 他プロバイダと意味を揃え、ツール呼び出しを強制する
		return &gemToolConfig{FunctionCallingConfig: gemFCCConfig{Mode: "ANY"}}
	case "none":
		return &gemToolConfig{FunctionCallingConfig: gemFCCConfig{Mode: "NONE"}}
	case "tool":
		if tc.Name == "" {
			// tool mode は tc.Name 必須。空指定は設定ミスとして警告ログを残し AUTO にフォールバックする
			slog.Warn("gemini: tool_choice mode=tool with empty Name, falling back to AUTO")
			return &gemToolConfig{FunctionCallingConfig: gemFCCConfig{Mode: "AUTO"}}
		}
		return &gemToolConfig{FunctionCallingConfig: gemFCCConfig{Mode: "ANY", AllowedFunctionNames: []string{tc.Name}}}
	default:
		slog.Warn("gemini: unknown tool_choice mode, falling back to AUTO", "mode", tc.Mode)
		return &gemToolConfig{FunctionCallingConfig: gemFCCConfig{Mode: "AUTO"}}
	}
}
