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

// pasteCoalesceWindow ペースト行結合の時間窓。端末ペーストの行間は数 ms、rlwrap は
// 一括書き込みのため 50ms で十分拾える。手入力で Enter 後 50ms 以内に次行を打ち終える
// ことは現実的にないため、連続質問が誤って結合されることはない。
const pasteCoalesceWindow = 50 * time.Millisecond

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

	pump := newBytePump(r.in)
	history := []llm.Message{}
	fmt.Fprintln(r.out, "go-llm-agent REPL  /quit で終了（生成中は ESC で中断）")

	// 端末入力のときだけ、短時間に連続到着した行 (改行込みペースト) を 1 プロンプトへ結合する。
	// パイプ入力は従来どおり 1 行 = 1 プロンプトを維持する。
	coalesce := time.Duration(0)
	if f, ok := r.in.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			coalesce = pasteCoalesceWindow
		}
	}

	for {
		fmt.Fprint(r.out, ">> ")
		line, err := pump.readPrompt(coalesce)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, errCtrlC) {
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

		turnMessages, quit := r.runTurn(ctx, pump, append([]llm.Message{}, history...))
		if len(turnMessages) == 0 {
			// 中断やエラーで何も生成されなかったターンは user 入力ごと巻き戻す。
			// content 空の assistant や user 連続を履歴に残すと、以後の全リクエストが
			// llama-server の履歴検証 (400) で失敗し続けるため。
			history = history[:len(history)-1]
		} else {
			history = append(history, turnMessages...)
		}
		if quit {
			return nil
		}
	}
}

// runTurn は 1 ターンを実行する。入力が端末なら raw 化し、pump 経由で届くバイトから
// ESC（ターン中断）/ Ctrl-C（中断して終了）を検出する。その他のバイトは次の行編集へ
// 引き継ぐ。返り値は履歴に積む assistant メッセージと終了フラグ。
func (r *REPL) runTurn(ctx context.Context, pump *bytePump, hist []llm.Message) ([]llm.Message, bool) {
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	out := r.out
	restoreRaw := func() {}
	if f, ok := r.in.(*os.File); ok {
		if restore, started := beginRaw(f); started {
			restoreRaw = restore
			// raw 中は出力後処理が無効なので、TTY 出力に限り単独 \n を \r\n に変換する
			if isTTY(r.out) {
				out = newCRLFWriter(r.out)
			}
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
	var turnMessages []llm.Message
	interrupted := false
	quit := false
	keyCh := pump.ch // 入力終端 (close) 検知後は nil 化して select から外す
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
				if len(ev.TurnMessages) > 0 {
					turnMessages = append([]llm.Message(nil), ev.TurnMessages...)
				} else if ev.Final != nil {
					turnMessages = []llm.Message{*ev.Final}
				}
				fmt.Fprintln(out)
			case agent.EventError:
				r.stopSpinner()
				// ESC / Ctrl-C 中断による context キャンセルはユーザー操作なのでエラー表示しない
				if interrupted && errors.Is(ev.Err, context.Canceled) {
					continue
				}
				fmt.Fprintf(out, "\n[error] %v\n", ev.Err)
			}
		case b, ok := <-keyCh:
			if !ok {
				keyCh = nil
				continue
			}
			switch b {
			case 0x1b: // ESC: このターンだけ中断
				if !interrupted {
					interrupted = true
					cancelTurn()
					r.stopSpinner()
					fmt.Fprint(out, "\n[中断しました]\n")
				}
			case 0x03: // Ctrl-C: 中断してセッション終了 (raw では SIGINT にならずキーとして届く)
				if !interrupted {
					interrupted = true
					cancelTurn()
					r.stopSpinner()
				}
				quit = true
			default:
				// 生成中に打たれたバイトは次の行読みへ引き継ぐ
				pump.pushback(b)
			}
		}
	}
	r.stopSpinner()
	restoreRaw() // cooked へ復帰。以降の出力は通常の \n でよい
	// 中断時は「[中断しました]」を既に出しているため done サマリは抑制する
	if !r.opt.DisableSpinner && !interrupted {
		fmt.Fprintf(r.out, "↳ done in %.1fs · %d tool · in %d / out %d tok\n",
			time.Since(turnStart).Seconds(), toolCount, usageIn, usageOut)
	}
	// EventFinal が届かないまま終わったターン (中断・エラー) は、部分生成テキストが
	// あるときだけ assistant として残す。空のまま返すと呼び出し側がターンごと巻き戻す。
	if len(turnMessages) == 0 && strings.TrimSpace(finalContent.String()) != "" {
		turnMessages = []llm.Message{{Role: llm.RoleAssistant, Content: finalContent.String()}}
	}
	return turnMessages, quit
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
