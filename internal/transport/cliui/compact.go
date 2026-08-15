package cliui

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/llm"
)

// compactTimeout 圧縮の要約呼び出しに許す最大時間。超過時は圧縮をスキップする。
// パッケージ変数にするのは、テストが短い値へ差し替えて上限超過の経路を
// 現実的な実行時間で踏めるようにするため。実行時に書き換える経路は無い
var compactTimeout = 60 * time.Second

// shouldCompact 直近ターンの最後の LLM 呼び出しの実測 input tokens が閾値以上かを返す。
// lastInputTokens == 0 を除外するのは、圧縮呼び出し自体が EventUsage を発生させないこと、
// およびエラーで即終了したターンで自動発火しないようにするため
func (r *REPL) shouldCompact(lastInputTokens int) bool {
	c := r.opt.Compaction
	if !c.Enabled || r.opt.Registry == nil || lastInputTokens == 0 {
		return false
	}
	threshold := int(float64(c.ContextWindowTokens) * c.TriggerRatio)
	return lastInputTokens >= threshold
}

// compactResult 要約 goroutine の戻り値
type compactResult struct {
	msgs []llm.Message
	err  error
}

// compactHistory 履歴を圧縮する。失敗・上限超過・中断のいずれでも元の hist を
// そのまま返し警告を表示する。要約呼び出しは別 goroutine で行い、その間も
// pump のバイトを監視して ESC / Ctrl-C を受け付ける (同期呼び出しにすると
// 圧縮中に pump を読む主体が存在せず REPL が中断不能になるため)
func (r *REPL) compactHistory(ctx context.Context, pump *bytePump, hist []llm.Message, out io.Writer) []llm.Message {
	prov, model, err := r.opt.Registry.Resolve(r.model)
	if err != nil {
		fmt.Fprintf(out, "[compact] 警告: モデル解決に失敗したため圧縮をスキップしました: %v\n", err)
		return hist
	}
	fmt.Fprintf(out, "[compact] 会話履歴を圧縮しています (最大 %s、ESC で中断)\n", compactTimeout)
	cctx, cancel := context.WithTimeout(ctx, compactTimeout)
	defer cancel()

	done := make(chan compactResult, 1)
	go func() {
		msgs, cerr := agent.CompactMessages(cctx, prov, model, hist, agent.CompactOptions{
			KeepRecentTurns: r.opt.Compaction.KeepRecentTurns,
		})
		done <- compactResult{msgs: msgs, err: cerr}
	}()

	// pump.ch を局所変数へ取り、close 後は nil にして select から外す。
	// close 済みチャネルは常に即座に選択されるため、nil 化しないとこの select が
	// 要約呼び出しの完了までホットループになる
	pumpCh := pump.ch
	for {
		select {
		case res := <-done:
			return r.reportCompactResult(res, hist, cctx.Err() != nil, out)
		case b, ok := <-pumpCh:
			if !ok {
				pumpCh = nil
				continue
			}
			// 圧縮中の打鍵は読み捨てる。pushback で次のプロンプトへ回すと
			// 利用者の意図しない行が送信されるため (生成中の扱いに揃える)
			if b == 0x1b || b == 0x03 {
				cancel()
				fmt.Fprintln(out, "[compact] 中断しました。元の履歴のまま続行します")
				return hist
			}
		}
	}
}

// reportCompactResult 圧縮結果を表示し、採用する履歴を返す。
// timedOut は圧縮用 ctx が期限切れだったかを表す
func (r *REPL) reportCompactResult(res compactResult, hist []llm.Message, timedOut bool, out io.Writer) []llm.Message {
	if res.err != nil {
		if timedOut {
			fmt.Fprintf(out, "[compact] 警告: 圧縮が %s を超えたためスキップしました\n", compactTimeout)
		} else {
			fmt.Fprintf(out, "[compact] 警告: 履歴圧縮に失敗したため元の履歴のまま続行します: %v\n", res.err)
		}
		return hist
	}
	if len(res.msgs) >= len(hist) {
		// CompactMessages は保持対象ターン数が実際のターン数以上のとき入力を
		// そのまま返す。この no-op を「圧縮しました」と表示すると圧縮の実施を確認できない
		fmt.Fprintln(out, "[compact] 圧縮対象がありません。履歴はそのままです")
		return hist
	}
	fmt.Fprintf(out, "[compact] 会話履歴を圧縮しました (%d件 -> %d件)\n", len(hist), len(res.msgs))
	return res.msgs
}
