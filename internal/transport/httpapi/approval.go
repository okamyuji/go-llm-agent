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
	if ok := s.approver.Submit(agent.ApprovalDecision{
		RunID:    runID,
		CallID:   body.CallID,
		Allowed:  body.Allowed,
		Reason:   body.Reason,
		Reviewer: body.Reviewer,
	}); !ok {
		// 監査ログ 該当する pending エントリがない場合の不正な Submit を記録する
		slog.WarnContext(r.Context(), "hitl approval submit failed",
			"run_id", runID,
			"call_id", body.CallID,
			"allowed", body.Allowed,
			"reviewer", body.Reviewer,
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
		"reviewer", body.Reviewer,
		"remote_addr", r.RemoteAddr,
	)
	w.WriteHeader(http.StatusNoContent)
}
