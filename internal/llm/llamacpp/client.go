// Package llamacpp は llama.cpp の llama-server (OpenAI 互換 API) 向けプロバイダー。
// openai パッケージとの違いは3点: (1) Authorization ヘッダを送らない (ローカル前提)、
// (2) cache_prompt=true を常時送りエージェントループの prefill 再利用を効かせる、
// (3) temperature / max_tokens を ChatRequest から passthrough する。
// tool call の JSON 構造は llama-server 側 (--jinja) が grammar で強制するため、
// クライアント側での schema 制約は行わない。
package llamacpp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// Options クライアント生成オプション
type Options struct {
	BaseURL               string
	HTTPClient            *http.Client
	RequestTimeoutSeconds int
	// Temperature 非 nil のとき全リクエストの既定 temperature に設定する。
	// リクエスト側 ChatRequest.Temperature が指定されていればそちらが優先する。
	Temperature *float64
	// MaxTokens 非 nil のとき既定の生成上限トークン数に設定する。
	// 1 応答が context を埋め尽くすまで走る暴走・長時間化を防ぐ。
	// リクエスト側 ChatRequest.MaxTokens が指定されていればそちらが優先する。
	MaxTokens *int
	// RepeatPenalty 非 nil のとき全リクエストに repeat_penalty を送る。
	// 量子化/abliterated モデルの繰り返し暴走を抑える (llama-server 対応パラメータ)。
	RepeatPenalty *float64
	// Think 非 nil のとき chat_template_kwargs.enable_thinking に設定する。
	// false で Qwen 系 reasoning モデルの thinking を抑制し、ツール呼び出しを速く安定させる。
	Think *bool
	// ToolCallIDFormat "alnum9" 指定時、ツール呼び出し ID を 9 文字英数字へ書き換える。
	// Mistral-Nemo 系テンプレートの「9 文字英数字」制約対策。空文字なら書き換えない。
	ToolCallIDFormat string
}

// Client llama-server の OpenAI 互換 Chat Completions API クライアント
type Client struct {
	baseURL       string
	http          *http.Client
	temperature   *float64
	maxTokens     *int
	repeatPenalty *float64
	think         *bool
	toolIDFormat  string
}

// ローカル推論は prefill が長引きやすいため、雲プロバイダーより長い既定タイムアウトを取る
const defaultTimeout = 300 * time.Second

const maxRequestTimeoutSeconds = 24 * 60 * 60

// New Options からクライアントを生成
func New(o Options) *Client {
	c := o.HTTPClient
	if c == nil {
		timeout := defaultTimeout
		if o.RequestTimeoutSeconds > 0 {
			sec := min(o.RequestTimeoutSeconds, maxRequestTimeoutSeconds)
			timeout = time.Duration(sec) * time.Second
		}
		c = &http.Client{Timeout: timeout}
	}
	if o.BaseURL == "" {
		o.BaseURL = "http://localhost:8080/v1"
	}
	return &Client{baseURL: o.BaseURL, http: c, temperature: o.Temperature, maxTokens: o.MaxTokens, repeatPenalty: o.RepeatPenalty, think: o.Think, toolIDFormat: o.ToolCallIDFormat}
}

// Name プロバイダー名を返す
func (c *Client) Name() string { return "llamacpp" }

type chatPayload struct {
	Model              string            `json:"model"`
	Messages           []chatPayloadMsg  `json:"messages"`
	Stream             bool              `json:"stream"`
	Tools              []chatPayloadTool `json:"tools,omitempty"`
	ToolChoice         any               `json:"tool_choice,omitempty"`
	CachePrompt        bool              `json:"cache_prompt"`
	Temperature        *float64          `json:"temperature,omitempty"`
	MaxTokens          *int              `json:"max_tokens,omitempty"`
	RepeatPenalty      *float64          `json:"repeat_penalty,omitempty"`
	ChatTemplateKwargs map[string]any    `json:"chat_template_kwargs,omitempty"`
}

// chatPayloadMsg の content は空文字でも必ず送る (omitempty 禁止)。
// llama-server は assistant メッセージに content か tool_calls のどちらかを必須とし、
// 両方欠けた {"role":"assistant"} は 400 invalid_request_error になるため。
type chatPayloadMsg struct {
	Role       string            `json:"role"`
	Content    string            `json:"content"`
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

// chatPayloadTool はツール宣言。tool-call とは別形状で、JSON Schema は parameters、
// 説明は description に入れる (OpenAI / llama-server 仕様)。
type chatPayloadTool struct {
	Type     string              `json:"type"`
	Function chatPayloadToolFunc `json:"function"`
}

type chatPayloadToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
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

// Chat 同期で llama-server に問い合わせる
func (c *Client) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	payload := c.toPayload(req, false)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("llamacpp marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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
			Underlying: fmt.Errorf("llamacpp http %d: %s", res.StatusCode, string(b)),
		}
	}

	var parsed chatResp
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("llamacpp decode: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("llamacpp: no choices")
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
			ID: c.normalizeToolID(tc.ID), Name: tc.Function.Name, Arguments: normalizeArgs(tc.Function.Arguments),
		})
	}
	return out, nil
}

// normalizeArgs は tool call の arguments をオブジェクト形式へ正規化する。
// OpenAI 互換仕様 (llama-server 含む) は arguments を二重エンコードの JSON文字列で返すが、
// 下流の tool.Execute / validator.Validate はオブジェクトへ Unmarshal するため、
// 文字列なら一段アンラップして中身のオブジェクトを取り出す。
// Ollama 等が返すオブジェクト形式や、未完成のフラグメントはそのまま返す。
func normalizeArgs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		// オブジェクト形式やフラグメントは文字列デコードに失敗する。そのまま返す
		return raw
	}
	// 空文字列の arguments は空オブジェクト扱いにして下流 Unmarshal を成立させる
	if s == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

func (c *Client) toPayload(req llm.ChatRequest, stream bool) chatPayload {
	temperature := req.Temperature
	if temperature == nil {
		// リクエスト側が未指定なら provider 既定を使う
		temperature = c.temperature
	}
	maxTokens := req.MaxTokens
	if maxTokens == nil {
		maxTokens = c.maxTokens
	}
	p := chatPayload{
		Model:         req.Model,
		Stream:        stream,
		CachePrompt:   true,
		Temperature:   temperature,
		MaxTokens:     maxTokens,
		RepeatPenalty: c.repeatPenalty,
	}
	if c.think != nil {
		// Qwen 系テンプレートの enable_thinking フラグ。false で thinking を抑制する
		p.ChatTemplateKwargs = map[string]any{"enable_thinking": *c.think}
	}
	for _, m := range req.Messages {
		pm := chatPayloadMsg{Role: string(m.Role), Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			args := tc.Arguments
			if len(args) == 0 {
				// nil/空 arguments は "arguments":null を避けて {} を送る (Jinja テンプレート対策)
				args = json.RawMessage(`{}`)
			}
			pm.ToolCalls = append(pm.ToolCalls, chatPayloadCall{
				ID: tc.ID, Type: "function",
				Function: chatPayloadFunc{Name: tc.Name, Arguments: args},
			})
		}
		p.Messages = append(p.Messages, pm)
	}
	for _, t := range req.Tools {
		p.Tools = append(p.Tools, chatPayloadTool{Type: "function", Function: chatPayloadToolFunc{Name: t.Name, Description: t.Description, Parameters: t.Schema}})
	}
	if req.ToolChoice != nil {
		p.ToolChoice = toolChoiceJSON(req.ToolChoice)
	}
	return p
}

// normalizeToolID は toolIDFormat に従いツール呼び出し ID を書き換える。
// "alnum9": 元 ID をハッシュして 9 文字英数字へ決定的に変換する
// (Mistral-Nemo 系テンプレートが tool_call_id に「9 文字英数字」を強制するため)。
// 空 ID や未設定時はそのまま返す。決定的なので、同一会話内で assistant.tool_calls[].id と
// tool メッセージの tool_call_id が一致し続ける。
func (c *Client) normalizeToolID(id string) string {
	if c.toolIDFormat != "alnum9" || id == "" {
		return id
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	sum := h.Sum64()
	var b [9]byte
	for i := range 9 {
		b[i] = alphabet[sum%uint64(len(alphabet))]
		sum /= uint64(len(alphabet))
		if sum == 0 {
			// エントロピー枯渇時は元 ID のバイトを混ぜて 9 文字を埋める
			sum = uint64(id[(i+1)%len(id)]) + 1
		}
	}
	return string(b[:])
}

// toolChoiceJSON ChatRequest.ToolChoice を OpenAI 互換仕様の値に変換する。
// llama-server は OpenAI 互換の tool_choice (auto/required/none/named) を受けるため
// openai プロバイダーと同じマッピングを維持する
func toolChoiceJSON(tc *llm.ToolChoice) any {
	switch tc.Mode {
	case "auto", "":
		return "auto"
	case "required", "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		if tc.Name == "" {
			slog.Warn("llamacpp: tool_choice mode=tool with empty Name, falling back to auto")
			return "auto"
		}
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}
	default:
		slog.Warn("llamacpp: unknown tool_choice mode, falling back to auto", "mode", tc.Mode)
		return "auto"
	}
}
