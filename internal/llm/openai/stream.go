package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

type streamReader struct {
	res    *http.Response
	scan   *bufio.Scanner
	closed bool
}

// Stream OpenAI に SSE で問い合わせ ChatStream を返す
func (c *Client) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	payload := toPayload(req, true)
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
	httpReq.Header.Set("Accept", "text/event-stream")

	res, err := c.http.Do(httpReq)
	if err != nil {
		return nil, &llm.ProviderError{Provider: c.Name(), Retryable: true, Underlying: err}
	}
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return nil, &llm.ProviderError{
			Provider: c.Name(), StatusCode: res.StatusCode,
			Retryable:  res.StatusCode == 429 || res.StatusCode >= 500,
			Underlying: fmt.Errorf("openai http %d: %s", res.StatusCode, string(b)),
		}
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	return &streamReader{res: res, scan: sc}, nil
}

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content   string            `json:"content"`
			ToolCalls []chatPayloadCall `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

// Recv 次の StreamEvent を返す。EOF は ok=false
func (r *streamReader) Recv() (llm.StreamEvent, bool) {
	if r.closed {
		return llm.StreamEvent{}, false
	}
	for r.scan.Scan() {
		line := strings.TrimSpace(r.scan.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return llm.StreamEvent{}, false
		}
		var c sseChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return llm.StreamEvent{Err: err}, true
		}
		ev := llm.StreamEvent{}
		if len(c.Choices) > 0 {
			ch := c.Choices[0]
			ev.DeltaText = ch.Delta.Content
			ev.Finish = ch.FinishReason
			for _, tc := range ch.Delta.ToolCalls {
				ev.ToolCall = &llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}
			}
		}
		if c.Usage != nil {
			ev.Usage = &llm.Usage{InputTokens: c.Usage.PromptTokens, OutputTokens: c.Usage.CompletionTokens}
		}
		return ev, true
	}
	return llm.StreamEvent{}, false
}

// Close ストリームを閉じる
func (r *streamReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.res.Body.Close()
}
