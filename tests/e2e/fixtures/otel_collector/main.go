// Package main 最小 OTLP HTTP コレクタを実装する
// /v1/traces と /v1/metrics を受け取り、受信ヒット数を stdout に書き出す
// E2E で OTel 連携をローカル PC 依存なく検証するために使う
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4318", "listen address")
	dur := flag.Duration("duration", 5*time.Second, "how long to run before reporting")
	flag.Parse()

	var traceHits, metricHits int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&traceHits, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/v1/metrics", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&metricHits, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(os.Stderr, "collector error:", err)
		}
	}()

	// duration 経過 または OS シグナルのいずれか早い方で graceful shutdown する
	// time.Sleep のブロックを select に置き換えて E2E 側からの中断にも応答する
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-time.After(*dur):
	case <-sigCh:
	}
	if err := srv.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "collector close:", err)
	}
	fmt.Printf("trace_hits=%d metric_hits=%d\n", atomic.LoadInt64(&traceHits), atomic.LoadInt64(&metricHits))
}
