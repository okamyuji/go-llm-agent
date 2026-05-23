package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/okamyuji/go-llm-agent/internal/agent"
)

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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
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
		http.Error(w, "no pending approval for runID/call_id", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
