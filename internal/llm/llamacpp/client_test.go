package llamacpp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/llamacpp"
)

func f64(v float64) *float64 { return &v }
func iptr(v int) *int        { return &v }

func TestChatSendsCachePromptAndSamplingParamsWithoutAuthHeader(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	res, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:       "shisa-12b",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Temperature: f64(0.2),
		MaxTokens:   iptr(256),
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.Message.Content != "ok" {
		t.Errorf("content = %q, want ok", res.Message.Content)
	}
	if res.Usage.InputTokens != 10 || res.Usage.OutputTokens != 2 {
		t.Errorf("usage = %+v, want 10/2", res.Usage)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header should be absent, got %q", gotAuth)
	}
	if v, ok := gotBody["cache_prompt"].(bool); !ok || !v {
		t.Errorf("cache_prompt should be true, body=%v", gotBody)
	}
	if v, ok := gotBody["temperature"].(float64); !ok || v != 0.2 {
		t.Errorf("temperature = %v, want 0.2", gotBody["temperature"])
	}
	if v, ok := gotBody["max_tokens"].(float64); !ok || v != 256 {
		t.Errorf("max_tokens = %v, want 256", gotBody["max_tokens"])
	}
}

func TestChatOmitsSamplingParamsWhenUnset(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	if _, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, exists := gotBody["temperature"]; exists {
		t.Errorf("temperature should be omitted when nil, body=%v", gotBody)
	}
	if _, exists := gotBody["max_tokens"]; exists {
		t.Errorf("max_tokens should be omitted when nil, body=%v", gotBody)
	}
}

func TestChatParsesToolCalls(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"fs_read","arguments":"{\"path\":\"a.txt\"}"}}]}}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	res, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:      "m",
		Messages:   []llm.Message{{Role: llm.RoleUser, Content: "read a.txt"}},
		Tools:      []llm.ToolSpec{{Name: "fs_read", Schema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &llm.ToolChoice{Mode: "required"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(res.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(res.Message.ToolCalls))
	}
	tc := res.Message.ToolCalls[0]
	if tc.ID != "c1" || tc.Name != "fs_read" || string(tc.Arguments) != `{"path":"a.txt"}` {
		t.Errorf("tool call = %+v", tc)
	}
	if gotBody["tool_choice"] != "required" {
		t.Errorf("tool_choice = %v, want required", gotBody["tool_choice"])
	}
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want 1 entry", gotBody["tools"])
	}
}

func TestChatNormalizesStringEncodedArgumentsToObject(t *testing.T) {
	// llama-server (OpenAI互換) は arguments を二重エンコードの JSON文字列で返す。
	// 下流の tool.Execute はオブジェクトへ Unmarshal するため、provider が一段アンラップして
	// オブジェクト形式へ正規化しなければツール実行が壊れる。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"fs_read","arguments":"{\"path\":\"a.txt\"}"}}]}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	res, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := string(res.Message.ToolCalls[0].Arguments); got != `{"path":"a.txt"}` {
		t.Errorf("arguments = %s, want normalized object {\"path\":\"a.txt\"}", got)
	}
	// 正規化後は下流と同じ手順でオブジェクトへ Unmarshal できること
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(res.Message.ToolCalls[0].Arguments, &params); err != nil {
		t.Fatalf("downstream unmarshal failed: %v", err)
	}
	if params.Path != "a.txt" {
		t.Errorf("path = %q, want a.txt", params.Path)
	}
}

func TestChatLeavesObjectFormArgumentsUnchanged(t *testing.T) {
	// Ollama 等はオブジェクト形式で返す。正規化はこれを壊してはならない。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"fs_read","arguments":{"path":"b.txt"}}}]}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	res, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(res.Message.ToolCalls[0].Arguments, &params); err != nil {
		t.Fatalf("downstream unmarshal failed: %v", err)
	}
	if params.Path != "b.txt" {
		t.Errorf("path = %q, want b.txt", params.Path)
	}
}

func TestStreamAccumulatesFragmentedToolCall(t *testing.T) {
	// 実測した llama-server の生SSE: 先頭フラグメントにのみ id/name、以降は index を頼りに
	// arguments を1〜数文字ずつ継続送信する。provider が index で連結し、各フラグメントの
	// arguments (JSON文字列断片) をデコードして結合しなければ、名前が空のまま loop に渡り
	// "tool \"\" が見つかりません" で壊れる。連結後の引数はオブジェクト形式になる。
	frags := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"q1","type":"function","function":{"name":"fs_read","arguments":"{"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"path"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":" \""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"note"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":".txt"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":9}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frags {
			_, _ = w.Write([]byte("data: " + f + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	st, err := c.Stream(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = st.Close() }()

	// loop と同じく「最後に見た ToolCall」を採用する
	var last *llm.ToolCall
	for {
		ev, ok := st.Recv()
		if !ok {
			break
		}
		if ev.ToolCall != nil {
			tc := *ev.ToolCall
			last = &tc
		}
	}
	if last == nil {
		t.Fatal("no tool call surfaced from fragmented stream")
	}
	if last.ID != "q1" {
		t.Errorf("id = %q, want q1", last.ID)
	}
	if last.Name != "fs_read" {
		t.Errorf("name = %q, want fs_read (empty name is the bug this fixes)", last.Name)
	}
	// 連結後はオブジェクト形式。下流と同じ手順で Unmarshal できること (空白差は許容)
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(last.Arguments, &params); err != nil {
		t.Fatalf("accumulated args not object-form (%s): %v", last.Arguments, err)
	}
	if params.Path != "note.txt" {
		t.Errorf("path = %q, want note.txt", params.Path)
	}
}

func TestStreamNormalizesStringEncodedToolCallArguments(t *testing.T) {
	// 単一チャンクで完全な tool_calls (arguments が二重エンコード文字列) を返すサーバーもある。
	// この場合も連結ロジックを通ってオブジェクト形式へ正規化される。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"fs_read\",\"arguments\":\"{\\\"path\\\":\\\"a.txt\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	st, err := c.Stream(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = st.Close() }()

	var got string
	for {
		ev, ok := st.Recv()
		if !ok {
			break
		}
		if ev.ToolCall != nil {
			got = string(ev.ToolCall.Arguments)
		}
	}
	if got != `{"path":"a.txt"}` {
		t.Errorf("stream tool call arguments = %s, want normalized {\"path\":\"a.txt\"}", got)
	}
}

func TestChatSendsEnableThinkingWhenThinkSet(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	think := false
	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL, Think: &think})
	if _, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	kw, ok := gotBody["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatalf("chat_template_kwargs missing, body=%v", gotBody)
	}
	if v, ok := kw["enable_thinking"].(bool); !ok || v != false {
		t.Errorf("enable_thinking = %v, want false", kw["enable_thinking"])
	}
}

func TestChatOmitsChatTemplateKwargsWhenThinkNil(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	if _, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, exists := gotBody["chat_template_kwargs"]; exists {
		t.Errorf("chat_template_kwargs should be omitted when Think nil, body=%v", gotBody)
	}
}

func alnum9(s string) bool {
	if len(s) != 9 {
		return false
	}
	for _, r := range s {
		isDigit := r >= '0' && r <= '9'
		isLower := r >= 'a' && r <= 'z'
		isUpper := r >= 'A' && r <= 'Z'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}

func TestChatNormalizesToolCallIDToAlnum9(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"q1ZtMMG02OcAV0mlO1jUyPAgMyDFN41K","type":"function","function":{"name":"fs_read","arguments":"{\"path\":\"a.txt\"}"}}]}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL, ToolCallIDFormat: "alnum9"})
	res, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	got := res.Message.ToolCalls[0].ID
	if !alnum9(got) {
		t.Errorf("normalized id = %q, want 9-char alphanumeric", got)
	}
}

func TestToolCallIDNormalizationIsDeterministic(t *testing.T) {
	body := `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"SAME_ORIGINAL_ID_32_CHARS_LONGXX","type":"function","function":{"name":"fs_read","arguments":"{}"}}]}}],"usage":{}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL, ToolCallIDFormat: "alnum9"})
	res1, _ := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	res2, _ := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if res1.Message.ToolCalls[0].ID != res2.Message.ToolCalls[0].ID {
		t.Errorf("same original id mapped to different values: %q vs %q", res1.Message.ToolCalls[0].ID, res2.Message.ToolCalls[0].ID)
	}
}

func TestToolCallIDPassthroughWhenNoFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"original-32-char-id","type":"function","function":{"name":"fs_read","arguments":"{}"}}]}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	res, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if res.Message.ToolCalls[0].ID != "original-32-char-id" {
		t.Errorf("id = %q, want unchanged original-32-char-id", res.Message.ToolCalls[0].ID)
	}
}

func TestStreamNormalizesToolCallIDToAlnum9(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"q1ZtMMG02OcAV0mlO1jUyPAgMyDFN41K\",\"type\":\"function\",\"function\":{\"name\":\"fs_read\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL, ToolCallIDFormat: "alnum9"})
	st, err := c.Stream(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = st.Close() }()
	var got string
	for {
		ev, ok := st.Recv()
		if !ok {
			break
		}
		if ev.ToolCall != nil {
			got = ev.ToolCall.ID
		}
	}
	if !alnum9(got) {
		t.Errorf("stream normalized id = %q, want 9-char alphanumeric", got)
	}
}

func TestChatHTTPErrorIsRetryableProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`model loading`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	_, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("want error on 503")
	}
	var pe *llm.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want ProviderError, got %T: %v", err, err)
	}
	if pe.Provider != "llamacpp" {
		t.Errorf("provider = %q, want llamacpp", pe.Provider)
	}
	if !pe.Retryable {
		t.Error("503 should be retryable")
	}
	if pe.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", pe.StatusCode)
	}
}

func TestChatClientErrorIsNotRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`bad request`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	_, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	var pe *llm.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want ProviderError, got %T: %v", err, err)
	}
	if pe.Retryable {
		t.Error("400 should not be retryable")
	}
}

func TestChatNoChoicesIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	if _, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err == nil {
		t.Fatal("want error when choices empty")
	}
}

func TestName(t *testing.T) {
	if got := llamacpp.New(llamacpp.Options{}).Name(); got != "llamacpp" {
		t.Errorf("Name() = %q, want llamacpp", got)
	}
}

func TestDefaultBaseURL(t *testing.T) {
	// BaseURL 未指定なら llama-server の既定エンドポイントに向く。
	// 実サーバーが無いので接続エラーになるが、そのエラーメッセージに既定URLが含まれることで確認する。
	c := llamacpp.New(llamacpp.Options{HTTPClient: &http.Client{}})
	_, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err == nil {
		t.Skip("unexpected: localhost:8080 answered; skip default-url assertion")
	}
	if !strings.Contains(err.Error(), "localhost:8080") {
		t.Errorf("error should mention default host localhost:8080, got: %v", err)
	}
}

func TestCustomTimeoutOption(t *testing.T) {
	// RequestTimeoutSeconds 指定時に New が custom timeout の http.Client を構築する経路を通す
	var served bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL, RequestTimeoutSeconds: 30})
	if _, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !served {
		t.Error("server was not reached")
	}
}

func TestToolChoiceMapping(t *testing.T) {
	cases := []struct {
		name string
		mode string
		tool string
		want any
	}{
		{"auto", "auto", "", "auto"},
		{"empty-defaults-auto", "", "", "auto"},
		{"required", "required", "", "required"},
		{"any-maps-required", "any", "", "required"},
		{"none", "none", "", "none"},
		{"tool-empty-name-falls-back-auto", "tool", "", "auto"},
		{"unknown-falls-back-auto", "bogus", "", "auto"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{}}`))
			}))
			defer srv.Close()

			c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
			_, err := c.Chat(context.Background(), llm.ChatRequest{
				Model:      "m",
				Messages:   []llm.Message{{Role: llm.RoleUser, Content: "x"}},
				ToolChoice: &llm.ToolChoice{Mode: tt.mode, Name: tt.tool},
			})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if gotBody["tool_choice"] != tt.want {
				t.Errorf("tool_choice = %v, want %v", gotBody["tool_choice"], tt.want)
			}
		})
	}
}

func TestToolChoiceNamedTool(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	if _, err := c.Chat(context.Background(), llm.ChatRequest{
		Model:      "m",
		Messages:   []llm.Message{{Role: llm.RoleUser, Content: "x"}},
		ToolChoice: &llm.ToolChoice{Mode: "tool", Name: "fs_read"},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	tcObj, ok := gotBody["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("tool_choice = %v, want object", gotBody["tool_choice"])
	}
	fn, ok := tcObj["function"].(map[string]any)
	if !ok || fn["name"] != "fs_read" {
		t.Errorf("tool_choice.function.name = %v, want fs_read", tcObj["function"])
	}
}

func TestChatNormalizesEmptyStringArgumentsToEmptyObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"noarg","arguments":""}}]}}],"usage":{}}`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	res, err := c.Chat(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := string(res.Message.ToolCalls[0].Arguments); got != `{}` {
		t.Errorf("empty-string arguments = %s, want {}", got)
	}
}

func TestStreamHTTPErrorIsProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	_, err := c.Stream(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	var pe *llm.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("want ProviderError, got %T: %v", err, err)
	}
	if !pe.Retryable || pe.StatusCode != http.StatusInternalServerError {
		t.Errorf("pe = %+v, want retryable 500", pe)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	st, err := c.Stream(context.Background(), llm.ChatRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("second Close should be nil, got %v", err)
	}
	// Close 後の Recv は EOF
	if _, ok := st.Recv(); ok {
		t.Error("Recv after Close should return ok=false")
	}
}

func TestStreamParsesSSEDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if v, ok := body["stream"].(bool); !ok || !v {
			t.Errorf("stream should be true, body=%v", body)
		}
		if v, ok := body["cache_prompt"].(bool); !ok || !v {
			t.Errorf("cache_prompt should be true in stream, body=%v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"he\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := llamacpp.New(llamacpp.Options{BaseURL: srv.URL})
	st, err := c.Stream(context.Background(), llm.ChatRequest{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = st.Close() }()

	var text string
	var finish string
	for {
		ev, ok := st.Recv()
		if !ok {
			break
		}
		if ev.Err != nil {
			t.Fatalf("stream event error: %v", ev.Err)
		}
		text += ev.DeltaText
		if ev.Finish != "" {
			finish = ev.Finish
		}
	}
	if text != "hello" {
		t.Errorf("streamed text = %q, want hello", text)
	}
	if finish != "stop" {
		t.Errorf("finish = %q, want stop", finish)
	}
}
