package cliui

import (
	"bufio"
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
// 端末では raw モードにし、日本語を桁ずれなく描画・↑↓履歴・生成中 ESC 中断を扱う。
func (r *REPL) Run(ctx context.Context) error {
	restore, raw := enableRawMode(r.in)
	defer restore()

	// raw モードでは出力後処理が無効なので、単独 \n を \r\n へ変換する
	effOut := r.out
	if raw {
		effOut = newCRLFWriter(r.out)
	}

	// スピナー: 注入があればそれを、無ければ TTY/raw のとき有効化して生成する
	if r.sp == nil && !r.opt.DisableSpinner {
		enabled := raw || isTTY(r.out)
		r.sp = NewSpinner(SpinnerOptions{Out: effOut, Model: r.opt.Model, Enabled: &enabled})
	}

	// 入力は 1 本の goroutine で読み、keyCh へ流す。行編集と生成中 ESC 監視が時分割で消費する。
	// raw 端末では、ESC 単独と矢印を区別するためタイムアウト付きバイトチャネル経由で解読する
	// (同期 ReadByte だと ESC の次バイトを待ってブロックし、単独 ESC が届かない)。
	var kr *keyReader
	if raw {
		byteCh := make(chan byte, 256)
		go func() {
			br := bufio.NewReader(r.in)
			for {
				b, err := br.ReadByte()
				if err != nil {
					close(byteCh)
					return
				}
				byteCh <- b
			}
		}()
		kr = newKeyReaderFromBytes(byteCh)
	} else {
		kr = newKeyReader(r.in)
	}
	keyCh := make(chan keyEvent, 64)
	go func() {
		for {
			ev, err := kr.readKey()
			if err != nil {
				close(keyCh)
				return
			}
			keyCh <- ev
		}
	}()
	src := &bufKeySource{ch: keyCh}
	editor := newLineEditorFromSource(src, effOut, ">> ")

	history := []llm.Message{}
	newline(effOut, "go-llm-agent REPL  /quit で終了")

	for {
		line, err := editor.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, errInterrupted) {
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

		assistant, quit := r.runTurn(ctx, effOut, keyCh, src, append([]llm.Message{}, history...))
		history = append(history, assistant)
		if quit {
			return nil
		}
	}
}

// runTurn は 1 ターンを実行する。生成中の keyCh を監視し、ESC でそのターンだけ中断する。
// 非 ESC キーは src へ退避し次の行編集へ引き継ぐ。返り値は履歴に積む assistant メッセージ。
func (r *REPL) runTurn(ctx context.Context, out io.Writer, keyCh <-chan keyEvent, src *bufKeySource, hist []llm.Message) (llm.Message, bool) {
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()
	quit := false

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
	kc := keyCh // close 検知後に nil 化して以降の select を無効化する
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
				// ESC 中断による context キャンセルはユーザー操作なのでエラー表示しない
				if interrupted && errors.Is(ev.Err, context.Canceled) {
					continue
				}
				fmt.Fprintf(out, "\n[error] %v\n", ev.Err)
			}
		case k, ok := <-kc:
			if !ok {
				kc = nil // 入力ストリーム終端。以降この case は選択されない
				continue
			}
			switch k.kind {
			case keyEsc:
				if !interrupted {
					interrupted = true
					cancelTurn()
					r.stopSpinner()
					fmt.Fprint(out, "\n[中断しました]\n")
				}
			case keyCtrlC:
				// raw モードでは ISIG 無効のため Ctrl-C は SIGINT にならずキーとして届く。
				// 生成を中断してセッションごと終了する (端末の慣習に合わせる)。
				if !interrupted {
					interrupted = true
					cancelTurn()
					r.stopSpinner()
				}
				quit = true
			default:
				// 生成中の非 ESC キーは次の行編集へ引き継ぐ
				src.pushback(k)
			}
		}
	}
	r.stopSpinner()
	// 中断時は「[中断しました]」を既に出しているため done サマリは抑制する
	if !r.opt.DisableSpinner && !interrupted {
		fmt.Fprintf(out, "↳ done in %.1fs · %d tool · in %d / out %d tok\n",
			time.Since(turnStart).Seconds(), toolCount, usageIn, usageOut)
	}
	return llm.Message{Role: llm.RoleAssistant, Content: finalContent.String()}, quit
}

// newline は raw モードでも桁が戻るよう本文の後に改行を出力する。
func newline(out io.Writer, s string) {
	fmt.Fprint(out, s+"\n")
}

// startSpinner sp が nil でなければスピナー描画を開始する
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
