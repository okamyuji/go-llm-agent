package cliui

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Phase スピナーが表示する作業フェーズ
type Phase int

// Phase 定数
const (
	PhaseThinking Phase = iota // LLM 応答待ち
	PhaseTool                  // ツール実行中
)

// SpinnerOptions Spinner 構築オプション
type SpinnerOptions struct {
	Out      io.Writer        // 既定 os.Stdout
	Now      func() time.Time // 既定 time.Now
	Interval time.Duration    // 既定 80ms
	Frames   []string         // 既定 10 フレームの braille
	Enabled  *bool            // nil なら Out の TTY 検出に従う
	Model    string           // PhaseThinking 表示用ラベル

	// OnRendered 1 回の render 完了後に呼ばれるフック（テスト用）
	// nil なら呼ばれない。本番コードでは設定しない
	OnRendered func()
}

// spinnerOp Spinner 制御コマンドの種別
type spinnerOp int

// spinnerOp 定数。ゼロ値は使わず必ず明示する
const (
	opSet spinnerOp = iota + 1
)

// spinnerCmd Spinner 制御コマンド
type spinnerCmd struct {
	op    spinnerOp
	phase Phase
	label string
}

// Spinner 1 行を上書きし続ける進捗インジケータ
type Spinner struct {
	out        io.Writer
	enabled    bool
	now        func() time.Time
	interval   time.Duration
	frames     []string
	model      string
	onRendered func()

	mu      sync.Mutex
	started bool
	cmds    chan spinnerCmd
	done    chan struct{}
}

// NewSpinner 与えた Options で Spinner を作る
func NewSpinner(opt SpinnerOptions) *Spinner {
	out := opt.Out
	if out == nil {
		out = os.Stdout
	}
	enabled := isTTY(out)
	if opt.Enabled != nil {
		enabled = *opt.Enabled
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	interval := opt.Interval
	if interval <= 0 {
		interval = 80 * time.Millisecond
	}
	frames := opt.Frames
	if len(frames) == 0 {
		frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	}
	return &Spinner{
		out:        out,
		enabled:    enabled,
		now:        now,
		interval:   interval,
		frames:     frames,
		model:      opt.Model,
		onRendered: opt.OnRendered,
	}
}

// Start 指定フェーズで描画ループを開始する。enabled=false の場合は no-op
// 既に started の場合は SetPhase に振り替える
func (s *Spinner) Start(phase Phase, label string) {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		s.SetPhase(phase, label)
		return
	}
	s.started = true
	s.cmds = make(chan spinnerCmd, 4)
	s.done = make(chan struct{})
	cmds := s.cmds
	done := s.done
	s.mu.Unlock()
	go s.loop(phase, label, cmds, done)
}

// SetPhase フェーズ・ラベルを変える。enabled=false または stopped 時は no-op
// バッファ満杯時は drop して呼出側をブロックさせない
func (s *Spinner) SetPhase(phase Phase, label string) {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	cmds := s.cmds
	s.mu.Unlock()
	select {
	case cmds <- spinnerCmd{op: opSet, phase: phase, label: label}:
	default:
	}
}

// Stop 行を消して描画ループを終了する。enabled=false または stopped 時は no-op
// cmds の close を停止シグナルとして使い、goroutine の終了を <-done で待ち合わせる
func (s *Spinner) Stop() {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	cmds := s.cmds
	done := s.done
	s.started = false
	s.mu.Unlock()
	close(cmds)
	<-done
	s.mu.Lock()
	s.cmds = nil
	s.done = nil
	s.mu.Unlock()
}

// loop 1 本のレンダー goroutine。io.Writer への書込は全てここからのみ
// cmds が close されたら行を消して終了する
func (s *Spinner) loop(initialPhase Phase, initialLabel string, cmds <-chan spinnerCmd, done chan<- struct{}) {
	defer close(done)
	start := s.now()
	phase := initialPhase
	label := initialLabel
	idx := 0
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.render(phase, label, start, idx)
	for {
		select {
		case c, ok := <-cmds:
			if !ok {
				s.clear()
				return
			}
			if c.op == opSet {
				phase = c.phase
				label = c.label
				start = s.now()
				idx = 0
				s.render(phase, label, start, idx)
			}
		case <-ticker.C:
			idx++
			s.render(phase, label, start, idx)
		}
	}
}

// render 現在のフレームを 1 行で出力する。\x1b[K で行末まで消去
// io.Writer の Write エラーはベストエフォート UI として無視する。
// OnRendered が設定されていれば描画完了を通知する（テスト用）
func (s *Spinner) render(phase Phase, label string, start time.Time, idx int) {
	frame := s.frames[idx%len(s.frames)]
	elapsed := s.now().Sub(start).Seconds()
	var line string
	switch phase {
	case PhaseTool:
		line = fmt.Sprintf("\r%s tool: %s  %.1fs\x1b[K", frame, label, elapsed)
	default:
		line = fmt.Sprintf("\r%s %s thinking…  %.1fs\x1b[K", frame, s.model, elapsed)
	}
	_, _ = io.WriteString(s.out, line)
	if s.onRendered != nil {
		s.onRendered()
	}
}

// clear 行頭に戻し、行末まで消去する。改行は出さない
func (s *Spinner) clear() {
	_, _ = io.WriteString(s.out, "\r\x1b[K")
}

// isTTY io.Writer が *os.File かつ CharDevice なら true
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
