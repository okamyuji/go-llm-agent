package obs

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// traceContextHandler slog.Handler を wrap し、レコードに trace_id と span_id を attribute として付与する
type traceContextHandler struct {
	inner slog.Handler
}

// NewTraceContextHandler slog.Handler を OTel trace 連携付きでラップする
// 入力 inner が nil の場合は io.Discard 相当のハンドラとして振る舞う
func NewTraceContextHandler(inner slog.Handler) slog.Handler {
	if inner == nil {
		return slog.NewJSONHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	return &traceContextHandler{inner: inner}
}

// Enabled inner の判定をそのまま委譲する
func (h *traceContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle context の SpanContext を読み trace_id と span_id を付与してから inner に渡す
func (h *traceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs inner にそのまま委譲する
func (h *traceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceContextHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup inner にそのまま委譲する
func (h *traceContextHandler) WithGroup(name string) slog.Handler {
	return &traceContextHandler{inner: h.inner.WithGroup(name)}
}

// discardWriter io.Discard 相当の最小書き込み先
type discardWriter struct{}

// Write 何も書き込まずに長さだけ返す
func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
