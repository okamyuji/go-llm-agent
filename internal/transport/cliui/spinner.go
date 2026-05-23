package cliui

import (
	"io"
	"os"
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
}

// Spinner 1 行を上書きし続ける進捗インジケータ
type Spinner struct {
	out      io.Writer
	enabled  bool
	now      func() time.Time
	interval time.Duration
	frames   []string
	model    string
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
		out:      out,
		enabled:  enabled,
		now:      now,
		interval: interval,
		frames:   frames,
		model:    opt.Model,
	}
}

// Start 指定フェーズで描画ループを開始する。enabled=false の場合は no-op
func (s *Spinner) Start(phase Phase, label string) {}

// SetPhase フェーズ・ラベルを変える。enabled=false の場合は no-op
func (s *Spinner) SetPhase(phase Phase, label string) {}

// Stop 行を消して描画ループを終了する。enabled=false の場合は no-op
func (s *Spinner) Stop() {}

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
