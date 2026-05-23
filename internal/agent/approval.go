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

// approvalKey RunID と CallID を結合した型安全なマップキー
// 文字列連結だと ID 内の ":" が衝突を生むため struct で持つ
type approvalKey struct {
	RunID  string
	CallID string
}

// HTTPApprover HTTP 経由で外部システムからの承認を受け取る Approver
// approvalKey をキーに channel を管理する
type HTTPApprover struct {
	mu          sync.Mutex
	pending     map[approvalKey]chan ApprovalDecision
	defaultDeny bool
}

// NewHTTPApprover HTTPApprover を構築する
// defaultDeny=true のとき、サブミットされない CallID は最終的に拒否扱いになる
func NewHTTPApprover(defaultDeny bool) *HTTPApprover {
	return &HTTPApprover{pending: map[approvalKey]chan ApprovalDecision{}, defaultDeny: defaultDeny}
}

// Request 承認待ち channel を登録し ctx 監視で Decision を待つ
// defaultDeny=false で timeout した場合は Allowed=true を返し、エラーは nil にする。
// Decision と error の意味的不一致を避ける
func (a *HTTPApprover) Request(ctx context.Context, r ApprovalRequest) (ApprovalDecision, error) {
	k := approvalKey{RunID: r.RunID, CallID: r.CallID}
	a.mu.Lock()
	ch, ok := a.pending[k]
	if !ok {
		ch = make(chan ApprovalDecision, 1)
		a.pending[k] = ch
	}
	a.mu.Unlock()

	cleanup := func() {
		a.mu.Lock()
		// 同じ channel のときだけ消す。Submit と Request 終了の race による誤削除を防ぐ
		if cur, ok := a.pending[k]; ok && cur == ch {
			delete(a.pending, k)
		}
		a.mu.Unlock()
	}

	select {
	case d := <-ch:
		cleanup()
		return d, nil
	case <-ctx.Done():
		cleanup()
		if a.defaultDeny {
			return ApprovalDecision{RunID: r.RunID, CallID: r.CallID, Allowed: false, Reason: "timeout default deny"}, ErrApprovalTimeout
		}
		// default allow は明示的な許可扱いとし、error は付けないことで呼び出し側の
		// errors.Is(err, ErrApprovalTimeout) と Allowed 判定の意味的不一致を避ける
		return ApprovalDecision{RunID: r.RunID, CallID: r.CallID, Allowed: true, Reason: "timeout default allow"}, nil
	}
}

// Submit 外部からの Decision を該当 channel に流し込む
// 未登録の RunID/CallID は false を返す
func (a *HTTPApprover) Submit(d ApprovalDecision) bool {
	k := approvalKey{RunID: d.RunID, CallID: d.CallID}
	a.mu.Lock()
	ch, ok := a.pending[k]
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

// AutoApprover テスト用に常に Allowed=true を返す Approver
type AutoApprover struct{ Allow bool }

// Request 設定された Allow をそのまま返す
func (a AutoApprover) Request(_ context.Context, r ApprovalRequest) (ApprovalDecision, error) {
	return ApprovalDecision{RunID: r.RunID, CallID: r.CallID, Allowed: a.Allow, Reason: "auto"}, nil
}
