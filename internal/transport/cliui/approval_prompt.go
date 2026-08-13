package cliui

import (
	"context"

	"github.com/okamyuji/go-llm-agent/internal/agent"
)

// approvalDeniedByTimeout ctx 打ち切り時に返す拒否理由。default_decision は deny 固定
const approvalDeniedByTimeout = "approval timed out; default_decision=deny"

// approvalAnswer 承認プロンプトへの応答。deny / 中断 / 終了を区別する
type approvalAnswer struct {
	allowed bool
	// reason 拒否理由。ユーザーの明示的な n は "denied by user"
	reason string
	// interrupted ESC。runTurn は cancelTurn() を呼びターンを中断する
	interrupted bool
	// quit Ctrl-C。runTurn は quit=true を返しセッションを終了する
	quit bool
}

// approvalPromptRequest runTurn へ渡す承認プロンプト依頼
type approvalPromptRequest struct {
	req agent.ApprovalRequest
	// ctx loop.go が WithTimeout したもの。runTurn はこれの Done も監視する
	ctx   context.Context
	reply chan<- approvalAnswer
}

// ApprovalPrompter agent.ApprovalDecider を実装する REPL 用対話承認
// Decide は pump を直接読まず、runTurn に読み取りを委譲する (バイト読み取りの
// 単一所有者を pump.ch の消費側である runTurn に保つため)
type ApprovalPrompter struct {
	requests chan approvalPromptRequest
}

// NewApprovalPrompter バッファ 0 のリクエストチャネルを持つ ApprovalPrompter を生成する。
// バッファを持たせないのは、ctx が Done になって Decide が離脱したあとも送信済みの
// 要求がチャネルに残留し、次のターンで無関係な承認プロンプトが表示されて
// ユーザーの入力を 1 行奪う事故を防ぐため
func NewApprovalPrompter() *ApprovalPrompter {
	return &ApprovalPrompter{requests: make(chan approvalPromptRequest)}
}

// requestsCh runTurn が select で読むための受信専用チャネルを返す。
// 非公開メソッドにするのは、返り値の要素型 approvalPromptRequest が非公開型であり、
// 公開メソッドが非公開型を返すと revive の unexported-return に抵触するため
func (p *ApprovalPrompter) requestsCh() <-chan approvalPromptRequest {
	return p.requests
}

// Decide runTurn へ依頼を送り、応答を待つ。ctx が Done になった場合は
// default_decision (deny) へ解決し (false, reason, nil) を返す。
// reply は要求ごとの使い捨てチャネル (バッファ 1) であり、Decide が離脱したあとに
// runTurn が応答を送っても goroutine が漏れない
func (p *ApprovalPrompter) Decide(ctx context.Context, req agent.ApprovalRequest) (bool, string, error) {
	reply := make(chan approvalAnswer, 1)
	select {
	case p.requests <- approvalPromptRequest{req: req, ctx: ctx, reply: reply}:
	case <-ctx.Done():
		return false, approvalDeniedByTimeout, nil
	}
	select {
	case ans := <-reply:
		if ans.allowed {
			return true, "", nil
		}
		return false, ans.reason, nil
	case <-ctx.Done():
		return false, approvalDeniedByTimeout, nil
	}
}
