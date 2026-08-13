package cliui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

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
	// HistoryFile 端末入力時の履歴永続化先。空ならセッション内のみ。
	// rlwrap -H の形式 (1 行 1 エントリ) と互換で、既存ファイルを引き継げる。
	HistoryFile string
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
// 端末入力では全期間 raw 化し、プロンプトは term.Terminal の行エディタで読む
// (履歴・矢印キー編集・bracketed paste 対応。カノニカルモードの 1024 バイト
// 行制限による長文ペーストの詰まりも回避する)。生成中は pump 経由で届く
// バイトから ESC / Ctrl-C を監視する。パイプ入力は 1 行 = 1 プロンプト。
func (r *REPL) Run(ctx context.Context) error {
	// スピナー: 注入があればそれを、無ければ TTY のとき有効化して生成する
	if r.sp == nil && !r.opt.DisableSpinner {
		enabled := isTTY(r.out)
		r.sp = NewSpinner(SpinnerOptions{Out: r.out, Model: r.opt.Model, Enabled: &enabled})
	}

	pump := newBytePump(r.in)
	history := []llm.Message{}

	// 端末入力なら raw 化して行エディタを用意する。out は raw 中の \n 出力のために
	// CRLF 変換で包む (term.Terminal 自身は \r\n を書くので二重変換にはならない)
	out := r.out
	var editor *term.Terminal
	if f, ok := r.in.(*os.File); ok {
		fd := int(f.Fd())
		if term.IsTerminal(fd) {
			if saved, err := term.MakeRaw(fd); err == nil {
				defer func() { _ = term.Restore(fd, saved) }()
				if isTTY(r.out) {
					out = newCRLFWriter(r.out)
				}
				editor = term.NewTerminal(struct {
					io.Reader
					io.Writer
				}{&pumpReader{p: pump, ctx: ctx}, out}, ">> ")
				if w, h, sizeErr := term.GetSize(fd); sizeErr == nil {
					_ = editor.SetSize(w, h)
				}
				editor.History = newFileHistory(r.opt.HistoryFile, historyMaxEntries)
				editor.SetBracketedPasteMode(true)
				defer editor.SetBracketedPasteMode(false)
			}
		}
	}
	fmt.Fprintln(out, "go-llm-agent REPL  /quit で終了（生成中は ESC で中断、複数行ペーストは Enter で送信）")

	for {
		var line string
		var err error
		if editor != nil {
			line, err = readEditorPrompt(editor)
		} else {
			fmt.Fprint(out, ">> ")
			line, err = pump.readPrompt(ctx)
		}
		if err != nil {
			if ctx.Err() != nil {
				// SIGINT 等で root context がキャンセルされた。ループを続けると
				// 以後の全ターンが即失敗し続けるため、ここできれいに終了する
				fmt.Fprintln(out, "\nシグナルを受信したため終了します")
				return nil
			}
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
		if ctx.Err() != nil {
			fmt.Fprintln(out, "\nシグナルを受信したため終了します")
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
				// SIGINT による root context キャンセルも同様 (呼び出し側が終了メッセージを出す)
				if ctx.Err() != nil {
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
	restoreRaw() // セッション開始時の端末モードへ復帰する
	// 中断時は「[中断しました]」を既に出しているため done サマリは抑制する。
	// セッション全体が raw のときのために CRLF 変換済みの out へ書く
	if !r.opt.DisableSpinner && !interrupted {
		fmt.Fprintf(out, "↳ done in %.1fs · %d tool · in %d / out %d tok\n",
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
