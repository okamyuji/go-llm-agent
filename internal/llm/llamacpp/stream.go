package llamacpp

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

	// outbox は生成順に emit すべきイベントの待ち行列。
	// テキストデルタは即時 push、ツール呼び出しは連結完了時にまとめて push する。
	outbox []llm.StreamEvent
	// toolAccs は index ごとのツール呼び出し断片。order は初出順を保つ。
	toolAccs map[int]*toolAcc
	order    []int
	// flushed はツール呼び出しを outbox へ流し終えたか。二重 flush を防ぐ
	flushed bool
	// scanErrSent は scan.Err() を一度 surface したか。二重送出を防ぐ
	scanErrSent bool
	// client は ID 正規化 (normalizeToolID) を呼ぶために保持する
	client *Client
}

// toolAcc は 1 つのツール呼び出しの断片を index 単位で蓄積する
type toolAcc struct {
	id   string
	name string
	args strings.Builder
}

// Stream llama-server に SSE で問い合わせ ChatStream を返す
func (c *Client) Stream(ctx context.Context, req llm.ChatRequest) (llm.ChatStream, error) {
	payload := c.toPayload(req, true)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("llamacpp marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
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
			Underlying: fmt.Errorf("llamacpp http %d: %s", res.StatusCode, string(b)),
		}
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	return &streamReader{res: res, scan: sc, toolAccs: map[int]*toolAcc{}, client: c}, nil
}

type sseToolCall struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content   string        `json:"content"`
			ToolCalls []sseToolCall `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

// Recv 次の StreamEvent を返す。EOF は ok=false。
// テキストデルタは受信順に返し、ツール呼び出しは index ごとに連結して
// 生成完了時 (finish_reason 受信時、または stream 終端) に完全な形で 1 回返す。
func (r *streamReader) Recv() (llm.StreamEvent, bool) {
	if r.closed {
		return llm.StreamEvent{}, false
	}
	for {
		if len(r.outbox) > 0 {
			ev := r.outbox[0]
			r.outbox = r.outbox[1:]
			return ev, true
		}
		if !r.scan.Scan() {
			// Scan() は clean EOF と read エラー (接続リセット、行長超過等) の両方で false を返す。
			// エラーを正常終了として握り潰さないよう scan.Err() を検査して surface する。
			if err := r.scan.Err(); err != nil && !r.scanErrSent {
				r.scanErrSent = true
				return llm.StreamEvent{Err: err}, true
			}
			// stream 終端: 未 flush のツール呼び出しがあれば流す
			if r.flushToolCalls() {
				continue
			}
			return llm.StreamEvent{}, false
		}
		line := strings.TrimSpace(r.scan.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			if r.flushToolCalls() {
				continue
			}
			return llm.StreamEvent{}, false
		}
		var c sseChunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return llm.StreamEvent{Err: err}, true
		}

		ev := llm.StreamEvent{}
		hasContent := false
		var finish string
		if len(c.Choices) > 0 {
			ch := c.Choices[0]
			if ch.Delta.Content != "" {
				ev.DeltaText = ch.Delta.Content
				hasContent = true
			}
			for _, tc := range ch.Delta.ToolCalls {
				r.accumulate(tc)
			}
			if ch.FinishReason != "" {
				finish = ch.FinishReason
				ev.Finish = ch.FinishReason
				hasContent = true
			}
		}
		if c.Usage != nil {
			ev.Usage = &llm.Usage{InputTokens: c.Usage.PromptTokens, OutputTokens: c.Usage.CompletionTokens}
			hasContent = true
		}
		if hasContent {
			r.outbox = append(r.outbox, ev)
		}
		// finish_reason を観測したら、蓄積済みツール呼び出しを完全な形で流す
		if finish != "" {
			r.flushToolCalls()
		}
	}
}

// accumulate はツール呼び出し断片を index 単位で蓄積する。
// 各断片の arguments は JSON 文字列でエンコードされた引数の一部なので、
// デコードして中身の文字列を連結する。文字列でなければ (オブジェクト一括送信) 生バイトを足す。
func (r *streamReader) accumulate(tc sseToolCall) {
	idx := 0
	if tc.Index != nil {
		idx = *tc.Index
	}
	acc, ok := r.toolAccs[idx]
	if !ok {
		acc = &toolAcc{}
		r.toolAccs[idx] = acc
		r.order = append(r.order, idx)
	}
	if tc.ID != "" {
		acc.id = tc.ID
	}
	if tc.Function.Name != "" {
		acc.name = tc.Function.Name
	}
	if len(tc.Function.Arguments) > 0 {
		var piece string
		if err := json.Unmarshal(tc.Function.Arguments, &piece); err == nil {
			acc.args.WriteString(piece)
		} else {
			acc.args.Write(tc.Function.Arguments)
		}
	}
}

// flushToolCalls は蓄積済みツール呼び出しを outbox へ完全な形で流す。
// 流すべきものがあれば true を返す。二重 flush は行わない
func (r *streamReader) flushToolCalls() bool {
	if r.flushed || len(r.order) == 0 {
		return false
	}
	r.flushed = true
	for _, idx := range r.order {
		acc := r.toolAccs[idx]
		args := normalizeArgs(json.RawMessage(acc.args.String()))
		if len(args) == 0 {
			// 引数フラグメントが皆無だった場合、下流 Unmarshal が成立するよう {} にする
			// (Chat 経路の空文字列 arguments と挙動を揃える)
			args = json.RawMessage(`{}`)
		}
		call := llm.ToolCall{
			ID:        r.client.normalizeToolID(acc.id),
			Name:      acc.name,
			Arguments: args,
		}
		r.outbox = append(r.outbox, llm.StreamEvent{ToolCall: &call, ToolCalls: []llm.ToolCall{call}})
	}
	return len(r.outbox) > 0
}

// Close ストリームを閉じる
func (r *streamReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.res.Body.Close()
}
