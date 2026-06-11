package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/okamyuji/go-llm-agent/internal/llm"
)

type streamReader struct {
	res    *http.Response
	scan   *bufio.Scanner
	closed bool
	tcIdx  int
}

// Stream Ollama に NDJSON で問い合わせ ChatStream を返す
func (c *Client) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	p := c.toPayload(req, true)
	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("ollama marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")

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
			Underlying: fmt.Errorf("ollama http %d: %s", res.StatusCode, string(b)),
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
		line := bytes.TrimSpace(r.scan.Bytes())
		if len(line) == 0 {
			continue
		}
		var parsed ollamaResp
		if err := json.Unmarshal(line, &parsed); err != nil {
			return llm.StreamEvent{Err: err}, true
		}
		ev := llm.StreamEvent{DeltaText: parsed.Message.Content}
		for _, tc := range parsed.Message.ToolCalls {
			r.tcIdx++
			ev.ToolCall = &llm.ToolCall{
				ID:        fmt.Sprintf("call_%d", r.tcIdx),
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			}
		}
		if parsed.Done {
			ev.Finish = parsed.DoneReason
			if ev.Finish == "" {
				ev.Finish = "stop"
			}
			ev.Usage = &llm.Usage{InputTokens: parsed.PromptEvalCount, OutputTokens: parsed.EvalCount}
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
