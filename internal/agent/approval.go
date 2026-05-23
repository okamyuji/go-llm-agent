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

// ErrApprovalAlreadyPending 同一 runID/callID に対する併走 Request を示す sentinel error
// 同じキーで複数の goroutine が同時に待機すると Submit が片方しか起こさない race のため拒否する
var ErrApprovalAlreadyPending = errors.New("approval: another request is already pending for this runID/callID")

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
// 同じ runID/callID に対する併走 Request は ErrApprovalAlreadyPending で拒否する
// (同一キーで複数 goroutine が待機すると Submit が一方の channel しか起こさず race を生む)
func (a *HTTPApprover) Request(ctx context.Context, r ApprovalRequest) (ApprovalDecision, error) {
	k := approvalKey{RunID: r.RunID, CallID: r.CallID}
	a.mu.Lock()
	if _, exists := a.pending[k]; exists {
		a.mu.Unlock()
		return ApprovalDecision{RunID: r.RunID, CallID: r.CallID, Allowed: false, Reason: "duplicate pending request"}, ErrApprovalAlreadyPending
	}
	ch := make(chan ApprovalDecision, 1)
	a.pending[k] = ch
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
		// ctx.Done と ch が同時に ready のとき select はランダムに片方を選ぶため、
		// ctx 経路に入った後でも channel に Decision が積まれている可能性をもう一度確認する
		// (非ブロッキング receive)。これにより Submit と timeout が同時に発火しても
		// 有効な Decision が捨てられないようにする
		select {
		case d := <-ch:
			cleanup()
			return d, nil
		default:
		}
		cleanup()
		return ApprovalDecision{RunID: r.RunID, CallID: r.CallID, Allowed: false, Reason: "timeout default deny"}, ErrApprovalTimeout
	}
}

// Submit 外部からの Decision を該当 channel に流し込む
// 未登録の RunID/CallID は false を返す
//
// 排他制御 mu を取った状態のままで lookup と送信を行うことで、Request 側の timeout 経路
// (cleanup → delete(a.pending, k)) との TOCTOU race を防ぐ
// チャネルバッファは Request 側で 1 を確保しているため Lock 内の send はブロックしない
// 送信成功時にはエントリを即時 delete することで「Submit と Request 終了の race による
// 二重送信や late delivery」を抑制する (Request 側の cleanup でも防御している二重防壁)
func (a *HTTPApprover) Submit(d ApprovalDecision) bool {
	k := approvalKey{RunID: d.RunID, CallID: d.CallID}
	a.mu.Lock()
	defer a.mu.Unlock()
	ch, ok := a.pending[k]
	if !ok {
		return false
	}
	select {
	case ch <- d:
		delete(a.pending, k)
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
