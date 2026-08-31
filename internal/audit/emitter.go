package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/safety"
)

// Options Emitter の設定
type Options struct {
	WALDir   string
	IggyURL  string
	PAT      string
	Stream   string
	Expiry   string
	Redactor safety.Redactor
}

// Emitter 監査イベントを WAL へ書き、送信 goroutine で Iggy へ送る。
// 生成時には何も作らず、最初のイベントで sync.Once により初期化する
type Emitter struct {
	opts    Options
	runID   string
	once    sync.Once
	initErr error
	lock    *os.File
	walMu   sync.Mutex
	wals    map[string]*walFile
	sender  *sender
	cancel  context.CancelFunc
	warned  sync.Map
}

// NewEmitter ファイルも goroutine も作らない
func NewEmitter(o Options) *Emitter {
	if o.WALDir == "" {
		home, _ := os.UserHomeDir()
		o.WALDir = filepath.Join(home, ".go-llm-agent", "audit-wal")
	}
	if o.IggyURL == "" {
		o.IggyURL = "http://127.0.0.1:3000"
	}
	if o.Stream == "" {
		o.Stream = "agent-audit"
	}
	if o.Expiry == "" {
		o.Expiry = "7776000000000" // 90 日 (マイクロ秒)。Iggy 0.8.0 は u64 を要求する
	}
	return &Emitter{opts: o, runID: uuid.NewString(), wals: map[string]*walFile{}}
}

// RunID プロセス起点ごとの UUID
func (e *Emitter) RunID() string { return e.runID }

func (e *Emitter) init() error {
	e.once.Do(func() {
		if err := os.MkdirAll(e.opts.WALDir, 0o700); err != nil {
			e.initErr = err
			return
		}
		lock, err := acquireRunLock(e.opts.WALDir, e.runID)
		if err != nil {
			e.initErr = err
			return
		}
		e.lock = lock
		ctx, cancel := context.WithCancel(context.Background())
		e.cancel = cancel
		e.sender = &sender{
			dir:    e.opts.WALDir,
			runID:  e.runID,
			client: newIggyClient(e.opts.IggyURL, e.opts.PAT, e.opts.Stream, e.opts.Expiry),
			wake:   make(chan struct{}, 1),
			done:   make(chan struct{}),
		}
		go e.sender.run(ctx)
	})
	return e.initErr
}

func (e *Emitter) wal(sessionID string) (*walFile, error) {
	e.walMu.Lock()
	defer e.walMu.Unlock()
	if w, ok := e.wals[sessionID]; ok {
		return w, nil
	}
	w, err := openWAL(e.opts.WALDir, sessionID, e.runID)
	if err != nil {
		return nil, err
	}
	e.wals[sessionID] = w
	return w, nil
}

func (e *Emitter) sessionFor(ctx context.Context) string {
	id, replaced := NormalizeSessionID(SessionIDFrom(ctx), e.runID)
	if replaced {
		if _, loaded := e.warned.LoadOrStore(id, true); !loaded {
			slog.Warn("audit: session id missing or invalid, using fallback", "session_id", id)
		}
	}
	return id
}

func (e *Emitter) emit(ctx context.Context, ev Event) {
	if e == nil {
		return
	}
	//nolint:contextcheck // init() の内部 context は sender goroutine の寿命を Emitter に紐付けるためのもので、
	// 個々の emit 呼び出しの ctx を継いでキャンセルされると送信ループごと止まってしまう。意図的に独立させる
	if err := e.init(); err != nil {
		slog.Error("audit: init failed, event dropped", "err", err)
		return
	}
	ev.V = 1
	ev.ID = uuid.NewString()
	ev.RunID = e.runID
	ev.SessionID = e.sessionFor(ctx)
	ev.TS = time.Now().UTC()
	w, err := e.wal(ev.SessionID)
	if err != nil {
		slog.Error("audit: open wal failed, event dropped", "err", err)
		return
	}
	if _, err := w.Append(ev); err != nil {
		slog.Error("audit: wal append failed, event dropped", "err", err)
		return
	}
	select {
	case e.sender.wake <- struct{}{}:
	default:
	}
}

func (e *Emitter) toolCallPayload(c llm.ToolCall, withID bool) ToolCallPayload {
	p := ToolCallPayload{Name: c.Name, Arguments: RedactJSON(c.Arguments, e.opts.Redactor)}
	if withID {
		p.ID = c.ID
	}
	return p
}

// LLMRequest messages の content と tool_calls[].arguments に redactor を通して記録する
func (e *Emitter) LLMRequest(ctx context.Context, provider, model string, req llm.ChatRequest) {
	if e == nil {
		return
	}
	p := LLMRequestPayload{Temperature: req.Temperature, MaxTokens: req.MaxTokens}
	for _, m := range req.Messages {
		mp := MessagePayload{Role: string(m.Role), Content: RedactString(m.Content, e.opts.Redactor), Name: m.Name, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			mp.ToolCalls = append(mp.ToolCalls, e.toolCallPayload(tc, true))
		}
		p.Messages = append(p.Messages, mp)
	}
	for _, t := range req.Tools {
		p.Tools = append(p.Tools, t.Name)
	}
	raw, _ := json.Marshal(p)
	e.emit(ctx, Event{Kind: KindLLMRequest, Provider: provider, Model: model, Payload: raw})
}

// LLMResponse content は結合後の全文に redactor をもう一度通す
func (e *Emitter) LLMResponse(ctx context.Context, provider, model, content string, call *llm.ToolCall, finish string, err error) {
	if e == nil {
		return
	}
	p := LLMResponsePayload{Content: RedactString(content, e.opts.Redactor), Finish: finish}
	ev := Event{Kind: KindLLMResponse, Provider: provider, Model: model}
	if call != nil {
		tc := e.toolCallPayload(*call, true)
		p.ToolCall = &tc
		ev.CallID = call.ID
	}
	if err != nil {
		p.Error = err.Error()
	}
	raw, _ := json.Marshal(p)
	ev.Payload = raw
	e.emit(ctx, ev)
}

// ToolCall 呼出の入口で記録する（承認判定より前）
func (e *Emitter) ToolCall(ctx context.Context, call llm.ToolCall) {
	if e == nil {
		return
	}
	raw, _ := json.Marshal(e.toolCallPayload(call, false))
	e.emit(ctx, Event{Kind: KindToolCall, CallID: call.ID, Payload: raw})
}

// ToolResult content は呼び手が redactor を通した後の値を渡す
func (e *Emitter) ToolResult(ctx context.Context, callID, name, content string, isError bool, d time.Duration) {
	if e == nil {
		return
	}
	raw, _ := json.Marshal(ToolResultPayload{Name: name, Content: content, IsError: isError, DurationMS: d.Milliseconds()})
	e.emit(ctx, Event{Kind: KindToolResult, CallID: callID, Payload: raw})
}

// Usage トークン使用量
func (e *Emitter) Usage(ctx context.Context, provider, model string, u llm.Usage) {
	if e == nil {
		return
	}
	raw, _ := json.Marshal(UsagePayload{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens})
	e.emit(ctx, Event{Kind: KindUsage, Provider: provider, Model: model, Payload: raw})
}

// Shutdown 送信中の 1 件を待ってから戻る。未初期化なら何もしない
func (e *Emitter) Shutdown(ctx context.Context) error {
	if e == nil || e.cancel == nil {
		return nil
	}
	e.cancel()
	select {
	case <-e.sender.done:
	case <-ctx.Done():
	}
	if e.lock != nil {
		_ = e.lock.Close()
	}
	return nil
}
