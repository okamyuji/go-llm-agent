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
// セキュリティ上 timeout 時は常に deny 扱いとして fail-closed で振る舞う
type HTTPApprover struct {
	mu      sync.Mutex
	pending map[approvalKey]chan ApprovalDecision
}

// NewHTTPApprover HTTPApprover を構築する
// timeout 時は常に deny を返す fail-closed 設計
// 旧 API シグネチャ NewHTTPApprover(defaultDeny bool) は fail-open 経路を生むため廃止した
func NewHTTPApprover() *HTTPApprover {
	return &HTTPApprover{pending: map[approvalKey]chan ApprovalDecision{}}
}

// Request 承認待ち channel を登録し ctx 監視で Decision を待つ
// timeout 時は Allowed=false の Decision と ErrApprovalTimeout を返す fail-closed 設計
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
		return ApprovalDecision{RunID: r.RunID, CallID: r.CallID, Allowed: false, Reason: "timeout default deny"}, ErrApprovalTimeout
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
