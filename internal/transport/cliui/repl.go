package cliui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// Options REPL 生成オプション
type Options struct {
	Model          string
	SystemPrompt   string
	MaxToolHops    int
	In             io.Reader
	Out            io.Writer
	Spinner        *Spinner // nil なら DisableSpinner=false 時に自動生成
	DisableSpinner bool     // true なら turn サマリも出力しない（旧来動作）
}

// REPL 対話型 CLI
type REPL struct {
	svc agent.Service
	opt Options
	in  io.Reader
	out io.Writer
	sp  *Spinner
}

// NewREPL Service とオプションから REPL を生成する
func NewREPL(svc agent.Service, opt Options) *REPL {
	in := opt.In
	if in == nil {
		in = os.Stdin
	}
	out := opt.Out
	if out == nil {
		out = os.Stdout
	}
	if opt.MaxToolHops <= 0 {
		opt.MaxToolHops = 8
	}
	return &REPL{svc: svc, opt: opt, in: in, out: out, sp: opt.Spinner}
}

// Run REPL ループを実行する。/quit・Ctrl-C・EOF で終了。
// 入力は cooked モードのまま端末と IME に任せ（日本語変換・折り返しを壊さない）、
// 生成中だけ raw 化して ESC / Ctrl-C を監視する。
func (r *REPL) Run(ctx context.Context) error {
	// スピナー: 注入があればそれを、無ければ TTY のとき有効化して生成する
	if r.sp == nil && !r.opt.DisableSpinner {
		enabled := isTTY(r.out)
		r.sp = NewSpinner(SpinnerOptions{Out: r.out, Model: r.opt.Model, Enabled: &enabled})
	}

	history := []llm.Message{}
	fmt.Fprintln(r.out, "go-llm-agent REPL  /quit で終了（生成中は ESC で中断）")

	for {
		fmt.Fprint(r.out, ">> ")
		line, err := readInputLine(r.in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "/quit" || line == "/exit" {
			return nil
		}
		history = append(history, llm.Message{Role: llm.RoleUser, Content: line})

		assistant, quit := r.runTurn(ctx, append([]llm.Message{}, history...))
		history = append(history, assistant)
		if quit {
			return nil
		}
	}
}

// runTurn は 1 ターンを実行する。入力が端末なら raw 化して ESC / Ctrl-C を監視し、
// ESC はそのターンだけ中断、Ctrl-C は中断してセッションを終了する。
// 返り値は履歴に積む assistant メッセージと終了フラグ。
func (r *REPL) runTurn(ctx context.Context, hist []llm.Message) (llm.Message, bool) {
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	// cancelKind は監視 goroutine からの通知。バッファ 1 で取りこぼしなし。
	type cancelEvent struct{ quit bool }
	cancelCh := make(chan cancelEvent, 1)
	notify := func(quit bool) {
		select {
		case cancelCh <- cancelEvent{quit: quit}:
		default:
		}
		cancelTurn()
	}

	out := r.out
	var rt *rawTurn
	if f, ok := r.in.(*os.File); ok {
		if t, started := beginRawTurn(f, func() { notify(false) }, func() { notify(true) }); started {
			rt = t
			// raw 中は出力後処理が無効なので単独 \n を \r\n に変換する
			out = newCRLFWriter(r.out)
		}
	}

	ch := make(chan agent.Event, 16)
	go func() {
		defer close(ch)
		if err := r.svc.Run(turnCtx, agent.Input{
			Model:        r.opt.Model,
			SystemPrompt: r.opt.SystemPrompt,
			Messages:     hist,
			MaxToolHops:  r.opt.MaxToolHops,
		}, ch); err != nil {
			ch <- agent.Event{Kind: agent.EventError, Err: err}
		}
	}()

	turnStart := time.Now()
	var toolCount, usageIn, usageOut int
	var finalContent strings.Builder
	interrupted := false
	quit := false
	r.startSpinner(PhaseThinking, "")

	for ch != nil {
		select {
		case ev, ok := <-ch:
			if !ok {
				ch = nil
				continue
			}
			switch ev.Kind {
			case agent.EventDelta:
				r.stopSpinner()
				_, _ = io.WriteString(out, ev.Delta)
				finalContent.WriteString(ev.Delta)
			case agent.EventToolCall:
				r.stopSpinner()
				name := ""
				if ev.ToolCall != nil {
					name = ev.ToolCall.Name
				}
				fmt.Fprintf(out, "\n[tool_call %s]\n", name)
				toolCount++
				r.startSpinner(PhaseTool, name)
			case agent.EventToolResult:
				r.stopSpinner()
				name := ""
				if ev.ToolResult != nil {
					name = ev.ToolResult.Name
				}
				fmt.Fprintf(out, "[tool_result %s]\n", name)
				r.startSpinner(PhaseThinking, "")
			case agent.EventUsage:
				if ev.Usage != nil {
					usageIn += ev.Usage.InputTokens
					usageOut += ev.Usage.OutputTokens
				}
			case agent.EventFinal:
				r.stopSpinner()
				fmt.Fprintln(out)
			case agent.EventError:
				r.stopSpinner()
				// ESC / Ctrl-C 中断による context キャンセルはユーザー操作なのでエラー表示しない
				if interrupted && errors.Is(ev.Err, context.Canceled) {
					continue
				}
				fmt.Fprintf(out, "\n[error] %v\n", ev.Err)
			}
		case c := <-cancelCh:
			if !interrupted {
				interrupted = true
				r.stopSpinner()
				if !c.quit {
					fmt.Fprint(out, "\n[中断しました]\n")
				}
			}
			if c.quit {
				quit = true
			}
		}
	}
	r.stopSpinner()
	if rt != nil {
		rt.end() // cooked へ復帰。以降の出力は通常の \n でよい
	}
	// 中断時は「[中断しました]」を既に出しているため done サマリは抑制する
	if !r.opt.DisableSpinner && !interrupted {
		fmt.Fprintf(r.out, "↳ done in %.1fs · %d tool · in %d / out %d tok\n",
			time.Since(turnStart).Seconds(), toolCount, usageIn, usageOut)
	}
	return llm.Message{Role: llm.RoleAssistant, Content: finalContent.String()}, quit
}

// startSpinner sp が nil でなければスピナー描画を開始する。
// スピナーは \r と \x1b[K しか出さないため raw モード中も出力先の差し替えは不要。
func (r *REPL) startSpinner(phase Phase, label string) {
	if r.sp == nil {
		return
	}
	r.sp.Start(phase, label)
}

// stopSpinner sp が nil でなければスピナー描画を停止する
func (r *REPL) stopSpinner() {
	if r.sp == nil {
		return
	}
	r.sp.Stop()
}
