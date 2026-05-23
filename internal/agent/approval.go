package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// ApprovalRequest 承認待ちのリクエスト本体
type ApprovalRequest struct {
	RunID     string
	CallID    string
	ToolName  string
	Arguments json.RawMessage
}

// ApprovalDecision 承認結果
type ApprovalDecision struct {
	RunID    string
	CallID   string
	Allowed  bool
	Reason   string
	Reviewer string
}

// Approver ツール実行前の人間承認を要求するインターフェース
// timeout は ctx で渡し、Decision.Allowed が false の場合は実行をスキップする
type Approver interface {
	Request(ctx context.Context, r ApprovalRequest) (ApprovalDecision, error)
}

// ErrApprovalTimeout 承認待ちが ctx で timeout したことを示す sentinel error
var ErrApprovalTimeout = errors.New("approval: timed out waiting for decision")

// HTTPApprover HTTP 経由で外部システムからの承認を受け取る Approver
// RunID をキーに sync.Map で channel を管理する
type HTTPApprover struct {
	mu          sync.Mutex
	pending     map[string]chan ApprovalDecision
	defaultDeny bool
}

// NewHTTPApprover HTTPApprover を構築する
// defaultDeny=true のとき、サブミットされない CallID は最終的に拒否扱いになる
func NewHTTPApprover(defaultDeny bool) *HTTPApprover {
	return &HTTPApprover{pending: map[string]chan ApprovalDecision{}, defaultDeny: defaultDeny}
}

// Request 承認待ち channel を登録し ctx 監視で Decision を待つ
func (a *HTTPApprover) Request(ctx context.Context, r ApprovalRequest) (ApprovalDecision, error) {
	a.mu.Lock()
	ch, ok := a.pending[a.key(r)]
	if !ok {
		ch = make(chan ApprovalDecision, 1)
		a.pending[a.key(r)] = ch
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, a.key(r))
		a.mu.Unlock()
	}()
	select {
	case d := <-ch:
		return d, nil
	case <-ctx.Done():
		if a.defaultDeny {
			return ApprovalDecision{RunID: r.RunID, CallID: r.CallID, Allowed: false, Reason: "timeout default deny"}, ErrApprovalTimeout
		}
		return ApprovalDecision{RunID: r.RunID, CallID: r.CallID, Allowed: true, Reason: "timeout default allow"}, ErrApprovalTimeout
	}
}

// Submit 外部からの Decision を該当 channel に流し込む
// 未登録の RunID/CallID は false を返す
func (a *HTTPApprover) Submit(d ApprovalDecision) bool {
	a.mu.Lock()
	ch, ok := a.pending[a.keyDec(d)]
	a.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- d:
		return true
	default:
		return false
	}
}

// key 内部マップキー
func (a *HTTPApprover) key(r ApprovalRequest) string     { return r.RunID + ":" + r.CallID }
func (a *HTTPApprover) keyDec(d ApprovalDecision) string { return d.RunID + ":" + d.CallID }

// AutoApprover テスト用に常に Allowed=true を返す Approver
type AutoApprover struct{ Allow bool }

// Request 設定された Allow をそのまま返す
func (a AutoApprover) Request(_ context.Context, r ApprovalRequest) (ApprovalDecision, error) {
	return ApprovalDecision{RunID: r.RunID, CallID: r.CallID, Allowed: a.Allow, Reason: "auto"}, nil
}
