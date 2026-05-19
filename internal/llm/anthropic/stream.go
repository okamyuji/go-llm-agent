package anthropic

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
	curTC  *llm.ToolCall
}

// Stream Anthropic に SSE で問い合わせ ChatStream を返す
func (c *Client) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	p := toPayload(req, true)
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
			Underlying: fmt.Errorf("anthropic http %d: %s", res.StatusCode, string(b)),
		}
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	return &streamReader{res: res, scan: sc}, nil
}

type sseEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block,omitempty"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason,omitempty"`
	} `json:"delta,omitempty"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// Recv 次の StreamEvent を返す。EOF は ok=false
func (r *streamReader) Recv() (llm.StreamEvent, bool) {
	if r.closed {
		return llm.StreamEvent{}, false
	}
	for r.scan.Scan() {
		line := strings.TrimSpace(r.scan.Text())
		if line == "" || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return llm.StreamEvent{Err: err}, true
		}
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				r.curTC = &llm.ToolCall{ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name, Arguments: json.RawMessage("{}")}
			}
		case "content_block_delta":
			if ev.Delta != nil {
				if ev.Delta.Type == "text_delta" {
					return llm.StreamEvent{DeltaText: ev.Delta.Text}, true
				}
				if ev.Delta.Type == "input_json_delta" && r.curTC != nil {
					r.curTC.Arguments = json.RawMessage(string(r.curTC.Arguments) + ev.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			if r.curTC != nil {
				tc := *r.curTC
				r.curTC = nil
				return llm.StreamEvent{ToolCall: &tc}, true
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				return llm.StreamEvent{Finish: ev.Delta.StopReason}, true
			}
		case "message_stop":
			return llm.StreamEvent{}, false
		}
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
