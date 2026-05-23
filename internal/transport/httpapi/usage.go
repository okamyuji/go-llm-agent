package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/billing"
)

// usageResponse /v1/usage のレスポンス本体
type usageResponse struct {
	Object string             `json:"object"`
	Scope  string             `json:"scope"`
	Key    string             `json:"key"`
	Total  billing.Snapshot   `json:"total"`
	Date   string             `json:"date,omitempty"`
	Items  []billing.Snapshot `json:"items,omitempty"`
}

// handleUsage GET /v1/usage?session=<id> または ?date=YYYY-MM-DD を返す
// billing.Accumulator が未設定の場合は空集計を返す
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	resp := usageResponse{Object: "usage"}
	switch {
	case q.Get("session") != "":
		resp.Scope = "session"
		resp.Key = q.Get("session")
		if s.billing != nil {
			resp.Total = s.billing.SessionTotal(resp.Key)
		}
	case q.Get("date") != "":
		date := q.Get("date")
		if _, err := time.Parse("2006-01-02", date); err != nil {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		resp.Scope = "date"
		resp.Key = date
		resp.Date = date
		if s.billing != nil {
			resp.Total = s.billing.DailyTotal(date)
		}
	default:
		http.Error(w, "session or date query parameter required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Content-Type ヘッダ送信後は http.Error でステータス変更できないため、
		// 運用者に伝える手段として slog.Warn のみ残す。レスポンス本文の途中切断は
		// クライアント側の JSON パースエラーで検出されることを期待する
		slog.WarnContext(r.Context(), "httpapi: failed to encode /v1/usage response", "err", err)
	}
}
