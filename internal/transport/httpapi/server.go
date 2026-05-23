package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/billing"
	"github.com/okamyuji/go-llm-agent/internal/config"
	"github.com/okamyuji/go-llm-agent/internal/safety"
)

// Server HTTP サーバ
//
// redactor は OpenAI 互換レスポンスの最終 content に再適用するための参照を保持する
// agent loop は EventDelta 単位で redact するが、PII や JWT が chunk 境界を跨ぐと
// 取りこぼすため、syncChat / streamChat の集約後にもう 1 度全文に対して redact を掛ける
type Server struct {
	svc       agent.Service
	cfg       *config.Config
	billing   billing.Accumulator
	approver  *agent.HTTPApprover
	redactor  safety.Redactor
	mux       *http.ServeMux
	auth      *BearerAuth
	limiter   *TokenBucketLimiter
	allowlist *AllowlistCIDR
	cors      *CORS
}

// New Service と config から Server を生成する
// acc が nil の場合は /v1/usage は 404 ではなく空集計を返す
func New(svc agent.Service, cfg *config.Config, acc billing.Accumulator) *Server {
	s := &Server{svc: svc, cfg: cfg, billing: acc, mux: http.NewServeMux()}
	s.mux.HandleFunc("/v1/chat/completions", s.handleChat)
	s.mux.HandleFunc("/v1/models", s.handleModels)
	s.mux.HandleFunc("/v1/usage", s.handleUsage)
	s.mux.HandleFunc("/v1/runs/", s.handleRunsApprove)
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return s
}

// WithApprover HTTP Approver を保持し /v1/runs/<id>/approve に接続する
func (s *Server) WithApprover(a *agent.HTTPApprover) *Server {
	s.approver = a
	return s
}

// WithRedactor チャンク境界跨ぎの取りこぼし対策で最終 content に再適用する Redactor を設定する
// nil の場合は再適用しない
func (s *Server) WithRedactor(r safety.Redactor) *Server {
	s.redactor = r
	return s
}

// WithMiddleware 認証・レート制限・CIDR allowlist・CORS を設定する
func (s *Server) WithMiddleware(auth *BearerAuth, limiter *TokenBucketLimiter, allowlist *AllowlistCIDR, cors *CORS) *Server {
	s.auth = auth
	s.limiter = limiter
	s.allowlist = allowlist
	s.cors = cors
	return s
}

// Handler http.Handler を返す
// 適用順は外側から CORS → Allowlist → RateLimit → Auth → mux
// CORS を最外層に置くことで OPTIONS プリフライトが Allowlist や Auth に阻まれない
// RateLimit を Auth より外に置くことで失敗した認証試行も rate limit の対象になる
// (不正なトークンで連打しても 429 が返るため認証 brute force を抑制できる)
// 各ミドルウェアが nil の場合はその層をスキップする。WithMiddleware を呼ばない経路でも panic させない
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	if s.auth != nil {
		h = s.auth.Handler(h)
	}
	if s.limiter != nil {
		h = s.limiter.Handler(h)
	}
	if s.allowlist != nil {
		h = s.allowlist.Handler(h)
	}
	if s.cors != nil {
		h = s.cors.Handler(h)
	}
	return h
}

// ListenAndServe アドレスで Server を起動する。acc / ap / rd はすべて nil 可
// config の auth / rate_limit / allowlist / cors 設定を解釈してミドルウェアを構築する
// rd は OpenAI 互換レスポンス (/v1/chat/completions, stream=false) の最終 content に
// chunk 境界跨ぎの取りこぼし対策で再適用する
func ListenAndServe(ctx context.Context, addr string, svc agent.Service, cfg *config.Config, acc billing.Accumulator, ap *agent.HTTPApprover, rd safety.Redactor) error {
	server := New(svc, cfg, acc).WithApprover(ap).WithRedactor(rd)
	auth, limiter, allowlist, cors, err := buildMiddleware(cfg)
	if err != nil {
		return err
	}
	server.WithMiddleware(auth, limiter, allowlist, cors)
	// ReadHeaderTimeout で Slowloris 対策、ReadTimeout で本体読込みの長時間占有を防ぐ
	// WriteTimeout は /v1/chat/completions の SSE が長時間続くため 0 のまま、
	// 代わりに IdleTimeout で idle 接続を 120 秒で回収する
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		// 親 ctx は既に Done なので継承不可。Shutdown のタイムアウトは
		// 独立した Background から発行する必要がある。
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx) //nolint:contextcheck // Shutdown は親 ctx から切り離した新規 ctx で実行する設計
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list"}
	for name := range s.cfg.Providers {
		out.Data = append(out.Data, model{ID: name + "/*", Object: "model", OwnedBy: name})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
