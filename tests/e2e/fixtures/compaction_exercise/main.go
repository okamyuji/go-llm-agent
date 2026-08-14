// Package main 01-compaction.md 5.4 節の会話履歴圧縮 (自動発火 / /compact 手動発火 /
// no-op) を検証するフィクスチャ。REPL の発火経路 (shouldCompact と /compact 分岐) を
// 実際に通し、CompactMessages を直接呼ぶ経路は判定根拠に使わない。
// stub LLM は D-17 に従い fixture 内の httptest サーバで立てる
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/llm/openai"
	"github.com/okamyuji/go-llm-agent/internal/tool"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui"
)

const (
	stubModel   = "openai/stub"
	summaryText = "要約: 利用者は動作確認を行っている。未解決の課題は無い。"
	compactedNG = "[compact] 圧縮対象がありません"
	compactedOK = "[compact] 会話履歴を圧縮しました"
	summaryMark = "[過去の会話の要約]"
)

// reqMsg stub が受け取ったリクエストのメッセージ 1 件
type reqMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type reqPayload struct {
	Stream   bool     `json:"stream"`
	Messages []reqMsg `json:"messages"`
}

// stub OpenAI 互換の stub LLM。stream=false を要約呼び出し (prov.Chat)、
// stream=true を通常ターン (prov.Stream) として区別する
type stub struct {
	srv *httptest.Server

	mu           sync.Mutex
	summaryCalls int
	streamCalls  int
	// usages ターンごとに返す InputTokens。範囲外のターンは末尾値を使う
	usages []int
	// streamReqs stream=true のリクエストで受け取ったメッセージ列
	streamReqs [][]reqMsg
}

// newStub usages で指定した InputTokens をターン順に返す stub を起動する
func newStub(usages []int) *stub {
	s := &stub{usages: usages}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *stub) Close() { s.srv.Close() }

func (s *stub) handle(w http.ResponseWriter, r *http.Request) {
	var p reqPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if p.Stream {
		s.handleStream(w, p)
		return
	}
	s.handleSummary(w)
}

// nextInputTokens ターン番号に応じた InputTokens を返し、stream 呼び出しを記録する
func (s *stub) nextInputTokens(msgs []reqMsg) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.streamCalls
	s.streamCalls++
	s.streamReqs = append(s.streamReqs, msgs)
	if len(s.usages) == 0 {
		return 1
	}
	if idx >= len(s.usages) {
		idx = len(s.usages) - 1
	}
	return s.usages[idx]
}

func (s *stub) handleStream(w http.ResponseWriter, p reqPayload) {
	in := s.nextInputTokens(p.Messages)
	w.Header().Set("Content-Type", "text/event-stream")
	chunks := []string{
		`{"choices":[{"delta":{"content":"はい"}}]}`,
		fmt.Sprintf(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":1}}`, in),
	}
	for _, c := range chunks {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (s *stub) handleSummary(w http.ResponseWriter) {
	s.mu.Lock()
	s.summaryCalls++
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": summaryText},
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
	})
}

func (s *stub) counts() (summary, stream int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.summaryCalls, s.streamCalls
}

// lastStreamMessages 最後の stream リクエストで stub が受け取ったメッセージ列を返す
func (s *stub) lastStreamMessages() []reqMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streamReqs) == 0 {
		return nil
	}
	return s.streamReqs[len(s.streamReqs)-1]
}

// runREPL stub 向けの REPL を 1 回起動し、出力を返す
func runREPL(ctx context.Context, s *stub, in string, comp cliui.CompactionOptions) (string, error) {
	client := openai.New(openai.Options{BaseURL: s.srv.URL, APIKey: "stub"})
	reg := llm.NewRegistry(map[string]llm.Provider{"openai": client})
	var buf bytes.Buffer
	opt := cliui.Options{
		Model:          stubModel,
		In:             strings.NewReader(in),
		Out:            &buf,
		DisableSpinner: true,
		Registry:       reg,
		Compaction:     comp,
	}
	err := cliui.NewREPL(agent.New(reg, tool.NewRegistry(nil, nil)), opt).Run(ctx)
	return buf.String(), err
}

// checkAuto 閾値超過による自動発火を確認する
func checkAuto(ctx context.Context, out io.Writer) error {
	s := newStub([]int{10, 60, 10})
	defer s.Close()
	got, err := runREPL(ctx, s, "いち\nに\nさん\n", cliui.CompactionOptions{
		Enabled: true, ContextWindowTokens: 100, TriggerRatio: 0.5, KeepRecentTurns: 1,
	})
	if err != nil {
		return fmt.Errorf("auto シナリオの Run: %w", err)
	}
	if !strings.Contains(got, compactedOK) {
		return fmt.Errorf("自動発火で圧縮されていない: %q", got)
	}
	if strings.Contains(got, compactedNG) {
		return fmt.Errorf("自動発火が no-op になった: %q", got)
	}
	if sum, _ := s.counts(); sum != 1 {
		return fmt.Errorf("summaryCalls=%d want 1", sum)
	}
	fmt.Fprintln(out, "compaction_auto=true")
	return nil
}

// parseCompactCounts "[compact] 会話履歴を圧縮しました (N件 -> M件)" の N と M を返す
func parseCompactCounts(output string) (before, after int, ok bool) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, compactedOK) {
			continue
		}
		if _, err := fmt.Sscanf(line[strings.Index(line, "("):], "(%d件 -> %d件)", &before, &after); err != nil {
			return 0, 0, false
		}
		return before, after, true
	}
	return 0, 0, false
}

// hasConsecutiveUser role が user のメッセージが連続していれば true
func hasConsecutiveUser(msgs []reqMsg) bool {
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Role == "user" && msgs[i-1].Role == "user" {
			return true
		}
	}
	return false
}

// hasSummaryUserMessage 要約文が user メッセージとして含まれていれば true
func hasSummaryUserMessage(msgs []reqMsg) bool {
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(m.Content, summaryMark) && strings.Contains(m.Content, summaryText) {
			return true
		}
	}
	return false
}

// checkManual /compact による手動発火と、圧縮結果が後続リクエストへ渡ることを確認する
func checkManual(ctx context.Context, out io.Writer) error {
	s := newStub([]int{10, 10, 10})
	defer s.Close()
	got, err := runREPL(ctx, s, "いち\nに\n/compact\nさん\n", cliui.CompactionOptions{
		Enabled: true, ContextWindowTokens: 100, TriggerRatio: 1.0, KeepRecentTurns: 1,
	})
	if err != nil {
		return fmt.Errorf("manual シナリオの Run: %w", err)
	}
	before, after, ok := parseCompactCounts(got)
	if !ok || after >= before {
		return fmt.Errorf("手動発火の件数表示が不正 (before=%d after=%d ok=%v): %q", before, after, ok, got)
	}
	sum, streams := s.counts()
	if sum != 1 {
		return fmt.Errorf("summaryCalls=%d want 1", sum)
	}
	if streams != 3 {
		return fmt.Errorf("streamCalls=%d want 3 (/compact 後のターンが継続していない)", streams)
	}
	fmt.Fprintln(out, "compaction_manual=true")

	last := s.lastStreamMessages()
	if hasConsecutiveUser(last) {
		return fmt.Errorf("圧縮後の履歴に user が連続している: %+v", last)
	}
	if !hasSummaryUserMessage(last) {
		return fmt.Errorf("圧縮後の履歴に要約が user メッセージとして無い: %+v", last)
	}
	fmt.Fprintln(out, "compaction_no_consecutive_user=true")
	return nil
}

// checkNoop 保持ターン数が実際のターン数以上のとき no-op として報告されることを確認する
func checkNoop(ctx context.Context, out io.Writer) error {
	s := newStub([]int{10})
	defer s.Close()
	got, err := runREPL(ctx, s, "いち\n/compact\n", cliui.CompactionOptions{
		Enabled: true, ContextWindowTokens: 100, TriggerRatio: 1.0, KeepRecentTurns: 4,
	})
	if err != nil {
		return fmt.Errorf("noop シナリオの Run: %w", err)
	}
	if !strings.Contains(got, compactedNG) {
		return fmt.Errorf("no-op が報告されていない: %q", got)
	}
	if strings.Contains(got, compactedOK) {
		return fmt.Errorf("no-op なのに圧縮したと報告された: %q", got)
	}
	if sum, _ := s.counts(); sum != 0 {
		return fmt.Errorf("summaryCalls=%d want 0", sum)
	}
	fmt.Fprintln(out, "compaction_noop_reported=true")
	return nil
}

func run(ctx context.Context, out io.Writer) error {
	if err := checkAuto(ctx, out); err != nil {
		return err
	}
	if err := checkManual(ctx, out); err != nil {
		return err
	}
	return checkNoop(ctx, out)
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "compaction_exercise:", err)
		os.Exit(1)
	}
}
