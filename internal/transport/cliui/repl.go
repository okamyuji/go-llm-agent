package cliui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/llm"
	"github.com/okamyuji/go-llm-agent/internal/transport/cliui/lineedit"
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
	// ApprovalPrompter 対話承認。nil なら対話承認なし (従来どおり)
	ApprovalPrompter *ApprovalPrompter
	// Registry 圧縮時の要約呼び出しに使う。nil なら圧縮を無効化する
	Registry llm.Registry
	// Compaction agent.compaction の値をそのまま運ぶ
	Compaction CompactionOptions
	// SessionsDir チャットセッション JSONL の保存先ディレクトリ。
	// 空文字列なら記録・再開のいずれも行わない (既存の RunOneShot 等は未設定のままでよい)
	SessionsDir string
	// SessionID 開始時に使うセッション ID。空文字列なら新規 ID を生成する。
	// -resume 時は cmd/agent が読み込んだ最新セッションの ID を渡す
	SessionID string
	// InitialHistory -resume で復元した既存メッセージ列。空なら通常の新規セッション
	InitialHistory []llm.Message
	// AgentsMDPath 読み込んだ AGENTS.md の絶対パス。空文字列なら未読込
	// (起動バナーに表示しない)
	AgentsMDPath string
	// AvailableModels /model の一覧表示に使う、プロバイダー名 → allow_models の対応。
	// allow_models が空スライスのプロバイダーは「制限なし」として表示する
	AvailableModels map[string][]string
	// Billing 構成済みの billing.Accumulator。nil なら /cost はトークン数のみ表示する
	Billing billing.Accumulator
}

// CompactionOptions agent.compaction の値をそのまま運ぶ。
// Enabled は bool とする。ポインタは config の decode でゼロ値と未指定を
// 区別するための都合であり、cliui へは確定済みの値だけを渡す
type CompactionOptions struct {
	Enabled             bool
	ContextWindowTokens int
	TriggerRatio        float64
	KeepRecentTurns     int
}

// turnUsage 1 ターン中に観測した usage。In / Out はターン内の全 LLM 呼び出しの
// 合計 (/cost 用)、LastIn は最後の LLM 呼び出しの InputTokens (圧縮の閾値判定用)
type turnUsage struct {
	In     int
	Out    int
	LastIn int
}

// REPL 対話型 CLI
type REPL struct {
	svc agent.Service
	opt Options
	in  io.Reader
	out io.Writer
	sp  *Spinner
	// model 実行時のモデル文字列。圧縮の要約呼び出しもこの値で解決する
	model string
	// sessionID 現在のアクティブなセッション ID。runTurn が agent.Input.SessionID
	// として使う (billing のセッション単位集計とキーを合わせるため)
	sessionID string
	// totalIn / totalOut このセッションの累計 input / output トークン (/cost 用)
	totalIn  int
	totalOut int
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
	r.model = r.opt.Model
	st := &replState{history: append([]llm.Message{}, r.opt.InitialHistory...)}

	rec := r.newSessionRecorder()

	out, editor, closeEditor := r.setupEditor(ctx, pump)
	defer closeEditor()
	fmt.Fprintln(out, "go-llm-agent REPL  /quit で終了（生成中は ESC で中断、複数行ペーストは Enter で送信、/tools off でツール無効化）")
	if r.opt.AgentsMDPath != "" {
		fmt.Fprintf(out, "AGENTS.md: %s を読み込みました\n", r.opt.AgentsMDPath)
	}

	for {
		line, err := r.readPromptLine(ctx, editor, pump, out)
		if err != nil {
			return r.endSession(ctx, err, out)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// "/" で始まる入力はコマンドとして処理し、LLM へは送らない。
		// タイプミス (/tool on 等) を質問として送ると、モデルが架空のツール実行計画
		// テキストを回答して履歴が汚染され、以後の回答がその書式を真似続けるため。
		if strings.HasPrefix(line, "/") {
			if r.handleSlashCommand(ctx, pump, line, st, out, rec) {
				return nil
			}
			continue
		}
		if r.executeTurn(ctx, pump, line, st, out, rec) {
			return nil
		}
	}
}

// executeTurn 1 行のユーザー入力に対する 1 ターンを実行し、履歴・セッション累計・
// セッション記録・圧縮判定までを行う。戻り値 true は REPL の終了を意味する
// (ユーザーの Ctrl-C か シグナル受信)
func (r *REPL) executeTurn(ctx context.Context, pump *bytePump, line string, st *replState, out io.Writer, rec *sessionWriter) bool {
	userMsg := llm.Message{Role: llm.RoleUser, Content: line}
	st.history = append(st.history, userMsg)

	turnMessages, quit, usage := r.runTurn(ctx, pump, append([]llm.Message{}, st.history...), st.toolChoice)
	// 中断されたターンでも、届いた usage 分は実際に課金済みなので累計へ積む。
	// 積まないと /cost が実際の請求より過小に表示される
	r.totalIn += usage.In
	r.totalOut += usage.Out
	r.commitTurn(st, turnMessages, userMsg, out, rec)
	if quit {
		return true
	}
	if ctx.Err() != nil {
		fmt.Fprintln(out, "\nシグナルを受信したため終了します")
		return true
	}
	if r.shouldCompact(usage.LastIn) {
		st.history = r.compactHistory(ctx, pump, st.history, out)
	}
	return false
}

// commitTurn ターンの生成結果を履歴とセッション記録へ反映する。
// 生成が空のターンは user 入力ごと巻き戻す
func (r *REPL) commitTurn(st *replState, turnMessages []llm.Message, userMsg llm.Message, out io.Writer, rec *sessionWriter) {
	if len(turnMessages) == 0 {
		// 中断やエラーで何も生成されなかったターンは user 入力ごと巻き戻す。
		// content 空の assistant や user 連続を履歴に残すと、以後の全リクエストが
		// llama-server の履歴検証 (400) で失敗し続けるため。記録もこのターン
		// 全体について行わない (巻き戻された user 行が JSONL に孤立して残ると、
		// 次回 -resume でその行が新規発話と連続し、同じ 400 連鎖に載るため)
		st.history = st.history[:len(st.history)-1]
		return
	}
	st.history = append(st.history, turnMessages...)
	r.recordSession(rec, out, userMsg)
	for _, m := range turnMessages {
		r.recordSession(rec, out, m)
	}
}

// newSessionRecorder r.opt.SessionsDir が設定されていれば sessionWriter を
// 構築し r.sessionID を初期化する。空文字列 (記録無効) なら nil を返す
func (r *REPL) newSessionRecorder() *sessionWriter {
	sessionID := r.opt.SessionID
	if r.opt.SessionsDir == "" {
		r.sessionID = sessionID
		return nil
	}
	if sessionID == "" {
		sessionID = newSessionID(r.opt.SessionsDir)
	}
	r.sessionID = sessionID
	return newSessionWriter(r.opt.SessionsDir, sessionID)
}

// recordSession rec が nil でなければ m を記録する。記録に失敗しても
// ターンの実行は継続し、out へエラーを表示するだけに留める
// (ディスク容量不足等でチャット自体が使えなくなることを避けるため)
func (r *REPL) recordSession(rec *sessionWriter, out io.Writer, m llm.Message) {
	if rec == nil {
		return
	}
	if err := rec.append(m); err != nil {
		fmt.Fprintf(out, "[session] 記録に失敗しました: %v\n", err)
	}
}

// replState セッション中にコマンドとターンが共有する状態
type replState struct {
	history []llm.Message
	// toolChoice nil = 設定既定。/tools off で mode none に切り替える
	toolChoice *llm.ToolChoice
}

// setupEditor 端末入力なら raw 化して行エディタを用意する。out は raw 中の \n 出力のために
// CRLF 変換で包む (lineedit.Terminal 自身は \r\n を書くので二重変換にはならない)。
// 端末でなければ行エディタは nil で、出力先は r.out のまま
func (r *REPL) setupEditor(ctx context.Context, pump *bytePump) (io.Writer, *lineedit.Terminal, func()) {
	fd, ok := r.terminalFD()
	if !ok {
		return r.out, nil, func() {}
	}
	saved, err := term.MakeRaw(fd)
	if err != nil {
		return r.out, nil, func() {}
	}
	return r.newEditor(ctx, pump, fd, saved)
}

// terminalFD 入力が端末ならその file descriptor を返す
func (r *REPL) terminalFD() (int, bool) {
	f, ok := r.in.(*os.File)
	if !ok {
		return 0, false
	}
	fd := int(f.Fd())
	return fd, term.IsTerminal(fd)
}

// newEditor raw 化済みの端末に対して行エディタと後始末関数を組み立てる
func (r *REPL) newEditor(ctx context.Context, pump *bytePump, fd int, saved *term.State) (io.Writer, *lineedit.Terminal, func()) {
	out := r.out
	if isTTY(r.out) {
		out = newCRLFWriter(r.out)
	}
	editor := lineedit.NewTerminal(struct {
		io.Reader
		io.Writer
	}{&pumpReader{p: pump, ctx: ctx}, out}, ">> ")
	if w, h, sizeErr := term.GetSize(fd); sizeErr == nil {
		_ = editor.SetSize(w, h)
	}
	editor.History = newFileHistory(r.opt.HistoryFile, historyMaxEntries)
	editor.SetBracketedPasteMode(true)
	return out, editor, func() {
		editor.SetBracketedPasteMode(false)
		_ = term.Restore(fd, saved)
	}
}

// readPromptLine 1 行のプロンプト入力を読む。端末なら行エディタ、パイプなら pump を使う
func (r *REPL) readPromptLine(ctx context.Context, editor *lineedit.Terminal, pump *bytePump, out io.Writer) (string, error) {
	if editor != nil {
		return readEditorPrompt(editor)
	}
	fmt.Fprint(out, ">> ")
	return pump.readPrompt(ctx)
}

// endSession プロンプト読み取りエラーからセッション終了の可否を決める。
// SIGINT 等の context キャンセルと EOF / Ctrl-C は正常終了、それ以外は err を返す
func (r *REPL) endSession(ctx context.Context, err error, out io.Writer) error {
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

// handleSlashCommand スラッシュコマンドを処理する。戻り値 true はセッション終了を意味する
func (r *REPL) handleSlashCommand(ctx context.Context, pump *bytePump, line string, st *replState, out io.Writer, rec *sessionWriter) bool {
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "/quit", "/exit":
		return true
	case "/clear":
		// 汚染された履歴からセッション再起動なしで復旧する手段。-resume で復元した
		// InitialHistory 由来のメッセージも破棄対象に含める (残す分岐は設けない。
		// /clear は「ここから新しい会話を始める」という単一の意味を持つ)
		st.history = st.history[:0]
		// 累計もリセットして、Billing の有無によらず /cost が「現在のセッションの
		// 累計」を指すようにする (Billing 経路は rotate 後の新 ID がゼロ値を返す)
		r.totalIn, r.totalOut = 0, 0
		if rec != nil {
			newID := rec.rotate()
			r.sessionID = newID
			fmt.Fprintf(out, "[clear] 会話履歴を破棄しました。新しいセッション %s を開始します\n", newID)
		} else {
			fmt.Fprintln(out, "[clear] 会話履歴を破棄しました")
		}
	case "/compact":
		if r.opt.Registry == nil {
			fmt.Fprintln(out, "[compact] Registry が未設定のため圧縮できません")
			break
		}
		st.history = r.compactHistory(ctx, pump, st.history, out)
	case "/tools", "/tool":
		r.handleToolsCommand(arg, st, out)
	case "/help":
		fmt.Fprint(out, helpText())
	case "/model":
		r.handleModelCommand(out, arg)
	case "/cost":
		r.handleCostCommand(out)
	default:
		fmt.Fprintf(out, "[コマンド] %s は未定義です。利用可能: /quit /exit /clear /tools off|on /help /model /compact /cost\n", name)
	}
	return false
}

// helpText 全スラッシュコマンドの一覧と一行説明を返す
func helpText() string {
	return `[help] 利用可能なコマンド:
  /help                   このヘルプを表示します
  /model [provider/name]  現在のモデルを表示、または指定したモデルへ切り替えます
  /compact                会話履歴を要約に置き換えて圧縮します
  /cost                   このセッションの累計トークン数と費用を表示します
  /clear                  会話履歴を破棄し、新しいセッションを開始します
  /tools off|on           ツール定義の送信を無効化/有効化します
  /quit, /exit            REPL を終了します
`
}

// handleModelCommand arg が空なら現在のモデルと利用可能モデル一覧を表示する。
// arg が "provider/name" 形式なら r.model を差し替える。実際にそのモデルが
// 解決可能かどうかの検証は行わない (D-18: 次ターンの通常経路に委ねる)
func (r *REPL) handleModelCommand(out io.Writer, arg string) {
	if arg == "" {
		fmt.Fprintf(out, "[model] 現在のモデル: %s\n", r.model)
		r.printAvailableModels(out)
		return
	}
	provider, name, ok := strings.Cut(arg, "/")
	if !ok || provider == "" || name == "" {
		fmt.Fprintf(out, "[model] %q は provider/name 形式ではありません（例: openai/gpt-4.1-mini）\n", arg)
		return
	}
	r.model = arg
	fmt.Fprintf(out, "[model] 次のターンから %s を使用します\n", arg)
}

// printAvailableModels プロバイダー名の昇順で allow_models を表示する。
// AvailableModels 未設定なら何も表示しない
func (r *REPL) printAvailableModels(out io.Writer) {
	if len(r.opt.AvailableModels) == 0 {
		return
	}
	fmt.Fprintln(out, "[model] 利用可能:")
	names := make([]string, 0, len(r.opt.AvailableModels))
	for name := range r.opt.AvailableModels {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		models := r.opt.AvailableModels[name]
		if len(models) == 0 {
			fmt.Fprintf(out, "  %s: (allow_models 未設定 — 任意のモデル名を指定可能)\n", name)
			continue
		}
		fmt.Fprintf(out, "  %s: %s\n", name, strings.Join(models, ", "))
	}
}

// handleCostCommand セッション累計トークン数と、billing が構成されていれば
// 円換算コストを表示する
func (r *REPL) handleCostCommand(out io.Writer) {
	if r.opt.Billing == nil {
		fmt.Fprintf(out, "[cost] このセッション: in %d / out %d tok（価格設定が無いため円換算はできません）\n", r.totalIn, r.totalOut)
		return
	}
	snap := r.opt.Billing.SessionTotal(r.sessionID)
	fmt.Fprintf(out, "[cost] このセッション: in %d / out %d tok ・ ¥%.2f\n", snap.InputTokens, snap.OutputTokens, snap.CostJPY)
}

// handleToolsCommand ツール定義をリクエストに含めるかをセッション中に切り替える。
// 小型ローカルモデルはツール定義があると履歴つき長文の指示追従を失うため、
// 翻訳・要約など純粋な対話では off が安定する (README 参照)
func (r *REPL) handleToolsCommand(arg string, st *replState, out io.Writer) {
	switch arg {
	case "off":
		st.toolChoice = &llm.ToolChoice{Mode: "none"}
		fmt.Fprintln(out, "[tools] off — ツール定義を送らずに応答します")
	case "on":
		st.toolChoice = nil
		fmt.Fprintln(out, "[tools] on — ツールを使用します")
	default:
		state := "on"
		if st.toolChoice != nil {
			state = "off"
		}
		fmt.Fprintf(out, "[tools] 現在: %s（/tools off | /tools on で切替）\n", state)
	}
}

// runTurn は 1 ターンを実行する。入力が端末なら raw 化し、pump 経由で届くバイトから
// ESC（ターン中断）/ Ctrl-C（中断して終了）を検出する。その他のバイトは次の行編集へ
// 引き継ぐ。返り値は履歴に積む assistant メッセージと終了フラグ。
func (r *REPL) runTurn(ctx context.Context, pump *bytePump, hist []llm.Message, toolChoice *llm.ToolChoice) (turnMessages []llm.Message, quit bool, usage turnUsage) {
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	out, restoreRaw := r.beginTurnRaw()

	// safeOut は EventDelta の書込みを rune 境界で保護する。crlfWriter (out) の
	// 内側 (生成側に近い側) に挟むことで、crlfWriter へ渡るバイト列が常に
	// 完結した UTF-8 であるという不変条件を保つ (05-streaming.md 3.1 節)
	st := &turnState{out: out, safeOut: newRuneSafeWriter(out)}

	ch := make(chan agent.Event, 16)
	go func() {
		defer close(ch)
		if err := r.svc.Run(turnCtx, agent.Input{
			Model:        r.model,
			SystemPrompt: r.opt.SystemPrompt,
			Messages:     hist,
			MaxToolHops:  r.opt.MaxToolHops,
			ToolChoice:   toolChoice,
			SessionID:    r.sessionID,
		}, ch); err != nil {
			ch <- agent.Event{Kind: agent.EventError, Err: err}
		}
	}()

	turnStart := time.Now()
	keyCh := pump.ch // 入力終端 (close) 検知後は nil 化して select から外す
	// nil チャネルは常にブロックするため、prompter 未設定なら select から外れる
	var reqCh <-chan approvalPromptRequest
	if r.opt.ApprovalPrompter != nil {
		reqCh = r.opt.ApprovalPrompter.requestsCh()
	}
	r.startSpinner(PhaseThinking, "")

	for ch != nil {
		select {
		case ev, ok := <-ch:
			if !ok {
				ch = nil
				continue
			}
			r.handleTurnEvent(ctx, ev, st)
		case b, ok := <-keyCh:
			if !ok {
				keyCh = nil
				continue
			}
			if r.handleTurnKey(b, pump, cancelTurn, st) {
				quit = true
			}
		case pr, ok := <-reqCh:
			if !ok {
				reqCh = nil
				continue
			}
			if r.handleApprovalPrompt(pr, pump, cancelTurn, st) {
				quit = true
			}
		}
	}
	r.stopSpinner()
	// ch が close のみで EventFinal も EventError も届かずにループを抜けた
	// 場合でも、safeOut に未出力バイトが残っていないことを保証する防御的な呼出し
	_ = st.safeOut.Flush()
	restoreRaw() // セッション開始時の端末モードへ復帰する
	return r.finishTurn(st, out, turnStart), quit, turnUsage{In: st.usageIn, Out: st.usageOut, LastIn: st.usageLastIn}
}

// finishTurn ターン終了時のサマリ表示を行い、履歴へ積むメッセージ列を返す
func (r *REPL) finishTurn(st *turnState, out io.Writer, turnStart time.Time) []llm.Message {
	// 中断時は「[中断しました]」を既に出しているため done サマリは抑制する。
	// セッション全体が raw のときのために CRLF 変換済みの out へ書く
	if !r.opt.DisableSpinner && !st.interrupted {
		fmt.Fprintf(out, "↳ done in %.1fs · %d tool · in %d / out %d tok\n",
			time.Since(turnStart).Seconds(), st.toolCount, st.usageIn, st.usageOut)
	}
	// EventFinal が届かないまま終わったターン (中断・エラー) は、部分生成テキストが
	// あるときだけ assistant として残す。空のまま返すと呼び出し側がターンごと巻き戻す。
	if len(st.turnMessages) == 0 && strings.TrimSpace(st.finalContent.String()) != "" {
		return []llm.Message{{Role: llm.RoleAssistant, Content: st.finalContent.String()}}
	}
	return st.turnMessages
}

// beginTurnRaw 入力が端末なら raw 化し、raw 中の出力先 (CRLF 変換込み) と
// セッション開始時の端末モードへ戻す restore 関数を返す。端末でなければ
// r.out と no-op の restore をそのまま返す
func (r *REPL) beginTurnRaw() (io.Writer, func()) {
	f, ok := r.in.(*os.File)
	if !ok {
		return r.out, func() {}
	}
	restore, started := beginRaw(f)
	if !started {
		return r.out, func() {}
	}
	out := r.out
	// raw 中は出力後処理が無効なので、TTY 出力に限り単独 \n を \r\n に変換する
	if isTTY(r.out) {
		out = newCRLFWriter(r.out)
	}
	return out, restore
}

// turnState runTurn の select ループ中に複数の分岐から更新される可変状態をまとめる。
// handleTurnEvent / handleTurnKey へ渡してループ本体の分岐数を下げるための分離
type turnState struct {
	out          io.Writer
	safeOut      *runeSafeWriter
	finalContent strings.Builder
	turnMessages []llm.Message
	toolCount    int
	usageIn      int
	usageOut     int
	// usageLastIn 最後に届いた EventUsage の InputTokens (圧縮の閾値判定用)
	usageLastIn int
	interrupted bool
}

// handleTurnEvent ch から届いた 1 件の agent.Event を処理し st を更新する
func (r *REPL) handleTurnEvent(ctx context.Context, ev agent.Event, st *turnState) {
	switch ev.Kind {
	case agent.EventDelta:
		r.stopSpinner()
		_, _ = st.safeOut.Write([]byte(ev.Delta))
		st.finalContent.WriteString(ev.Delta)
	case agent.EventToolCall:
		r.stopSpinner()
		_ = st.safeOut.Flush()
		name := ""
		if ev.ToolCall != nil {
			name = ev.ToolCall.Name
		}
		fmt.Fprintf(st.out, "\n[tool_call %s]\n", name)
		st.toolCount++
		r.startSpinner(PhaseTool, name)
	case agent.EventToolResult:
		r.stopSpinner()
		_ = st.safeOut.Flush()
		name := ""
		if ev.ToolResult != nil {
			name = ev.ToolResult.Name
		}
		fmt.Fprintf(st.out, "[tool_result %s]\n", name)
		r.startSpinner(PhaseThinking, "")
	case agent.EventUsage:
		if ev.Usage != nil {
			st.usageIn += ev.Usage.InputTokens
			st.usageOut += ev.Usage.OutputTokens
			st.usageLastIn = ev.Usage.InputTokens
		}
	case agent.EventFinal:
		r.stopSpinner()
		_ = st.safeOut.Flush()
		if len(ev.TurnMessages) > 0 {
			st.turnMessages = append([]llm.Message(nil), ev.TurnMessages...)
		} else if ev.Final != nil {
			st.turnMessages = []llm.Message{*ev.Final}
		}
		fmt.Fprintln(st.out)
	case agent.EventError:
		r.stopSpinner()
		_ = st.safeOut.Flush()
		if suppressTurnError(ctx, st.interrupted, ev.Err) {
			return
		}
		fmt.Fprintf(st.out, "\n[error] %v\n", ev.Err)
	}
}

// suppressTurnError ユーザー操作 (ESC/Ctrl-C) や SIGINT による context キャンセルは
// エラー表示しない。ESC/Ctrl-C 中断は呼び出し元が「[中断しました]」を既に出しており、
// SIGINT は呼び出し側が終了メッセージを出すため
func suppressTurnError(ctx context.Context, interrupted bool, err error) bool {
	if interrupted && errors.Is(err, context.Canceled) {
		return true
	}
	return ctx.Err() != nil
}

// handleTurnKey 生成中に届いた 1 バイトを処理する。ESC はこのターンだけ中断し、
// Ctrl-C は中断のうえセッション終了を要求する (戻り値 true)。それ以外のバイトは
// 次の行編集へ引き継ぐ
func (r *REPL) handleTurnKey(b byte, pump *bytePump, cancelTurn context.CancelFunc, st *turnState) bool {
	switch b {
	case 0x1b: // ESC: このターンだけ中断
		if !st.interrupted {
			st.interrupted = true
			cancelTurn()
			r.stopSpinner()
			_ = st.safeOut.Flush()
			fmt.Fprint(st.out, "\n[中断しました]\n")
		}
		return false
	case 0x03: // Ctrl-C: 中断してセッション終了 (raw では SIGINT にならずキーとして届く)
		if !st.interrupted {
			st.interrupted = true
			cancelTurn()
			r.stopSpinner()
			_ = st.safeOut.Flush()
		}
		return true
	default:
		// 生成中に打たれたバイトは次の行読みへ引き継ぐ
		pump.pushback(b)
		return false
	}
}

// handleApprovalPrompt 承認要求を表示し 1 行応答を読んで prompter へ返す。
// 戻り値 true はセッション終了要求 (Ctrl-C) を意味する
func (r *REPL) handleApprovalPrompt(pr approvalPromptRequest, pump *bytePump, cancelTurn context.CancelFunc, st *turnState) bool {
	r.stopSpinner()
	// 未出力の delta バイトを先に吐く
	_ = st.safeOut.Flush()
	fmt.Fprintf(st.out, "\n[approval] tool=%s\n%s\napprove? [y/N] ", pr.req.ToolName, pr.req.Summary)
	ans := r.readApprovalAnswer(pump, pr.ctx)
	select {
	case pr.reply <- ans:
	case <-pr.ctx.Done():
	}
	switch {
	case ans.quit:
		fmt.Fprintln(st.out, "[approval] denied (セッションを終了します)")
		st.interrupted = true
		cancelTurn()
		return true
	case ans.interrupted:
		fmt.Fprintln(st.out, "[approval] denied (ターンを中断しました)")
		st.interrupted = true
		cancelTurn()
	case ans.allowed:
		fmt.Fprintln(st.out, "[approval] approved")
		r.startSpinner(PhaseThinking, "")
	default:
		fmt.Fprintln(st.out, "[approval] denied")
		r.startSpinner(PhaseThinking, "")
	}
	return false
}

// readApprovalAnswer は承認プロンプトへの 1 行応答を読み、deny / 中断 / 終了の
// 3 状態を区別して返す。"y"/"yes" のときだけ allowed=true とする。
// ESC (0x1b) は interrupted、Ctrl-C (0x03) は quit とし、いずれも deny を伴う。
// ctx が Done (timeout・ターン中断) の場合と空文字・EOF は単純な deny として扱う。
// この関数はターン実行中 pump を読む唯一の経路であり、他の goroutine が同時に
// pump.ch を読むことはない (呼び出し元 runTurn の select ループ自身が
// 一時的にこの関数へ読み取りを譲るため)
func (r *REPL) readApprovalAnswer(pump *bytePump, ctx context.Context) approvalAnswer {
	line, err := pump.readAnswerLine(ctx)
	if err != nil {
		switch {
		case errors.Is(err, errCtrlC):
			return approvalAnswer{reason: "interrupted by Ctrl-C", quit: true}
		case errors.Is(err, errESC):
			return approvalAnswer{reason: "interrupted by ESC", interrupted: true}
		default:
			return approvalAnswer{reason: "no answer; default_decision=deny"}
		}
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return approvalAnswer{allowed: true}
	default:
		return approvalAnswer{reason: "denied by user"}
	}
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
