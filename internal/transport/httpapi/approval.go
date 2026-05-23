package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/agent"
)

// maxApprovalBodyBytes /v1/runs/<id>/approve のリクエストボディ上限 (1MiB)
// 過大なペイロードによるメモリ枯渇を防ぐ
const maxApprovalBodyBytes int64 = 1 << 20

// approveRequest /v1/runs/<id>/approve のリクエストペイロード
type approveRequest struct {
	CallID   string `json:"call_id"`
	Allowed  bool   `json:"allowed"`
	Reason   string `json:"reason"`
	Reviewer string `json:"reviewer"`
}

// handleRunsApprove POST /v1/runs/<id>/approve を処理する
// path 形式: /v1/runs/<runID>/approve のみ受け付ける
// セキュリティ前提
//   - 認証は外側の BearerAuth ミドルウェアで担保される
//   - クライアントが reviewer フィールドを偽装した場合に備え、サーバ側でも reviewer の妥当性を最低限検査する
//     (制御文字混入や過大長を拒否、空欄なら "anonymous" に正規化する)
//   - 認証主体を context から取得する将来的な仕組み (例: BearerAuth が context に ID を載せる) は
//     現状未実装で、設計書 08 番にも記載した。reviewer はあくまで監査ログ用のラベルとして扱う
func (s *Server) handleRunsApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.approver == nil {
		http.Error(w, "approver is not configured", http.StatusNotFound)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/runs/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "approve" {
		http.Error(w, "expected /v1/runs/<runID>/approve", http.StatusBadRequest)
		return
	}
	runID := parts[0]
	var body approveRequest
	limited := io.LimitReader(r.Body, maxApprovalBodyBytes)
	if err := json.NewDecoder(limited).Decode(&body); err != nil {
		http.Error(w, "invalid json body (or body exceeds 1MiB)", http.StatusBadRequest)
		return
	}
	if body.CallID == "" {
		http.Error(w, "call_id required", http.StatusBadRequest)
		return
	}
	reviewer, ok := sanitizeReviewer(body.Reviewer)
	if !ok {
		http.Error(w, "reviewer field contains control chars or exceeds 128 bytes", http.StatusBadRequest)
		return
	}
	if ok := s.approver.Submit(agent.ApprovalDecision{
		RunID:    runID,
		CallID:   body.CallID,
		Allowed:  body.Allowed,
		Reason:   body.Reason,
		Reviewer: reviewer,
	}); !ok {
		// 監査ログ 該当する pending エントリがない場合の不正な Submit を記録する
		slog.WarnContext(r.Context(), "hitl approval submit failed",
			"run_id", runID,
			"call_id", body.CallID,
			"allowed", body.Allowed,
			"reviewer", reviewer,
			"remote_addr", r.RemoteAddr,
		)
		http.Error(w, "no pending approval for runID/call_id", http.StatusConflict)
		return
	}
	// 監査ログ 承認操作の事実を構造化ログとして残す
	// reviewer はクライアント自己申告値である点に注意し、別途認証層 (BearerAuth など) で
	// 呼び出し主体を制限する運用が前提となる
	slog.InfoContext(r.Context(), "hitl approval submitted",
		"run_id", runID,
		"call_id", body.CallID,
		"allowed", body.Allowed,
		"reason", body.Reason,
		"reviewer", reviewer,
		"remote_addr", r.RemoteAddr,
	)
	w.WriteHeader(http.StatusNoContent)
}

// sanitizeReviewer reviewer フィールドを監査ログ用に最低限正規化する
// 空欄は "anonymous" に置き換え、128 バイト超や制御文字混入は拒否する
func sanitizeReviewer(v string) (string, bool) {
	const maxLen = 128
	if v == "" {
		return "anonymous", true
	}
	if len(v) > maxLen {
		return "", false
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return v, true
}
