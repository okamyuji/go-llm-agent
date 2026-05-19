package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/okamyuji/go-llm-agent/internal/agent"
	"github.com/okamyuji/go-llm-agent/internal/config"
)

// Server HTTP サーバ
type Server struct {
	svc agent.Service
	cfg *config.Config
	mux *http.ServeMux
}

// New Service と config から Server を生成する
func New(svc agent.Service, cfg *config.Config) *Server {
	s := &Server{svc: svc, cfg: cfg, mux: http.NewServeMux()}
	s.mux.HandleFunc("/v1/chat/completions", s.handleChat)
	s.mux.HandleFunc("/v1/models", s.handleModels)
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	return s
}

// Handler http.Handler を返す
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe アドレスで Server を起動する
func ListenAndServe(ctx context.Context, addr string, svc agent.Service, cfg *config.Config) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           New(svc, cfg).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shCtx)
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
