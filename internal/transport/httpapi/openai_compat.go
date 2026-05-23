package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

type chatReq struct {
	Model    string       `json:"model"`
	Messages []chatReqMsg `json:"messages"`
	Stream   bool         `json:"stream"`
}

type chatReqMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

type chatRespMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRespChoice struct {
	Index        int         `json:"index"`
	Message      chatRespMsg `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatResp struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []chatRespChoice `json:"choices"`
}

type sseChoice struct {
	Index        int            `json:"index"`
	Delta        map[string]any `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type sseChunk struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []sseChoice `json:"choices"`
}

// maxChatBodyBytes /v1/chat/completions のリクエストボディ上限
// 巨大ペイロードによるメモリ枯渇 DoS を防ぐため http.MaxBytesReader でクランプする
const maxChatBodyBytes = 4 << 20

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxChatBodyBytes)
	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Model == "" {
		req.Model = s.cfg.DefaultModel
	}

	msgs := make([]llm.Message, 0, len(req.Messages))
	var sysPrompt string
	for _, m := range req.Messages {
		if m.Role == "system" {
			if sysPrompt != "" {
				sysPrompt += "\n"
			}
			sysPrompt += m.Content
			continue
		}
		msgs = append(msgs, llm.Message{Role: llm.Role(m.Role), Content: m.Content, Name: m.Name})
	}

	in := agent.Input{
		Model:        req.Model,
		SystemPrompt: sysPrompt,
		Messages:     msgs,
		MaxToolHops:  s.cfg.Agent.MaxToolHops,
	}

	if req.Stream {
		s.streamChat(r.Context(), w, in)
		return
	}
	s.syncChat(r.Context(), w, in)
}

func (s *Server) syncChat(ctx context.Context, w http.ResponseWriter, in agent.Input) {
	ch := make(chan agent.Event, 16)
	go func() {
		_ = s.svc.Run(ctx, in, ch)
		close(ch)
	}()
	var content strings.Builder
	for ev := range ch {
		if ev.Kind == agent.EventDelta {
			content.WriteString(ev.Delta)
		}
		if ev.Kind == agent.EventError {
			http.Error(w, ev.Err.Error(), http.StatusBadGateway)
			return
		}
	}
	final := content.String()
	// agent loop は EventDelta 単位で redact するため、PII / JWT 等が chunk 境界を
	// 跨ぐと取りこぼす。non-stream のレスポンスでは集約後の最終文字列に再度 Redactor を
	// 適用して安全側に倒す。stream 経路は SSE の即時性と引き換えに chunk 単位 redact のみ
	if s.redactor != nil {
		final = s.redactor.Redact(final)
	}
	out := chatResp{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   in.Model,
		Choices: []chatRespChoice{{
			Index:        0,
			Message:      chatRespMsg{Role: "assistant", Content: final},
			FinishReason: "stop",
		}},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) streamChat(ctx context.Context, w http.ResponseWriter, in agent.Input) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	created := time.Now().Unix()
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	writeSSE := func(chunk sseChunk) {
		b, _ := json.Marshal(chunk)
		_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	ch := make(chan agent.Event, 16)
	go func() {
		_ = s.svc.Run(ctx, in, ch)
		close(ch)
	}()
	stopReason := "stop"
	for ev := range ch {
		switch ev.Kind {
		case agent.EventDelta:
			writeSSE(sseChunk{
				ID: id, Object: "chat.completion.chunk", Created: created, Model: in.Model,
				Choices: []sseChoice{{Index: 0, Delta: map[string]any{"content": ev.Delta}}},
			})
		case agent.EventError:
			stopReason = "error"
		}
	}
	finish := stopReason
	writeSSE(sseChunk{
		ID: id, Object: "chat.completion.chunk", Created: created, Model: in.Model,
		Choices: []sseChoice{{Index: 0, Delta: map[string]any{}, FinishReason: &finish}},
	})
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
}
