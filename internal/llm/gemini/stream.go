package gemini

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

type streamReader struct {
	res    *http.Response
	scan   *bufio.Scanner
	closed bool
	tcIdx  int
}

// Stream Gemini に SSE で問い合わせ ChatStream を返す
func (c *Client) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	p := toPayload(req)
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("gemini marshal: %w", err)
	}
	endpoint := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s",
		c.baseURL, url.PathEscape(req.Model), url.QueryEscape(c.apiKey))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
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
			Underlying: fmt.Errorf("gemini http %d: %s", res.StatusCode, string(b)),
		}
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	return &streamReader{res: res, scan: sc}, nil
}

// Recv 次の StreamEvent を返す
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
		var parsed gemResponse
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			return llm.StreamEvent{Err: err}, true
		}
		if len(parsed.Candidates) == 0 {
			continue
		}
		cand := parsed.Candidates[0]
		ev := llm.StreamEvent{Finish: cand.FinishReason}
		for _, part := range cand.Content.Parts {
			if part.Text != "" {
				ev.DeltaText += part.Text
			}
			if part.FunctionCall != nil {
				r.tcIdx++
				tc := llm.ToolCall{
					ID:        fmt.Sprintf("call_%d", r.tcIdx),
					Name:      part.FunctionCall.Name,
					Arguments: part.FunctionCall.Args,
				}
				ev.ToolCall = &tc
			}
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
