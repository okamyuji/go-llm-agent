package obs

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeOTLPCollector OTLP HTTP の /v1/traces /v1/metrics を 200 OK で受け取るだけのモック
func fakeOTLPCollector(t *testing.T) (endpoint string, counter *int64, cleanup func()) {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Logf("collector body copy failed: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	host := strings.TrimPrefix(srv.URL, "http://")
	return host, &hits, srv.Close
}

func TestInitTelemetry_DisabledReturnsNoopShutdown(t *testing.T) {
	t.Parallel()

	sd, err := InitTelemetry(context.Background(), TelemetryConfig{Enabled: false}, newDiscardLogger())
	if err != nil {
		t.Fatalf("InitTelemetry returned unexpected error: %v", err)
	}
	if sd == nil {
		t.Fatal("InitTelemetry returned nil Shutdown for disabled config")
	}
	if err := sd(context.Background()); err != nil {
		t.Fatalf("noop Shutdown must not return error, got %v", err)
	}
}

func TestInitTelemetry_NilLoggerIsAccepted(t *testing.T) {
	t.Parallel()

	sd, err := InitTelemetry(context.Background(), TelemetryConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("InitTelemetry returned unexpected error with nil logger: %v", err)
	}
	if sd == nil {
		t.Fatal("InitTelemetry returned nil Shutdown for nil logger case")
	}
	if err := sd(context.Background()); err != nil {
		t.Fatalf("noop Shutdown must not return error, got %v", err)
	}
}

func TestInitTelemetry_DisabledFastPathNeverFails(t *testing.T) {
	t.Parallel()

	for i := range 10 {
		sd, err := InitTelemetry(context.Background(), TelemetryConfig{Enabled: false}, nil)
		if err != nil {
			t.Fatalf("disabled init must never error, got %v on iteration %d", err, i)
		}
		if sd == nil {
			t.Fatalf("disabled init must not return nil shutdown on iteration %d", i)
		}
	}
}

func TestInitTelemetry_EnabledExportsToFakeCollector(t *testing.T) {
	endpoint, hits, cleanup := fakeOTLPCollector(t)
	defer cleanup()

	ctx := context.Background()
	cfg := TelemetryConfig{
		Enabled:                true,
		Endpoint:               endpoint,
		Insecure:               true,
		SampleRatio:            1.0,
		ServiceName:            "test-svc",
		MetricsIntervalSeconds: 1,
	}
	sd, err := InitTelemetry(ctx, cfg, newDiscardLogger())
	if err != nil {
		t.Fatalf("InitTelemetry returned unexpected error: %v", err)
	}
	if sd == nil {
		t.Fatal("InitTelemetry returned nil shutdown for enabled config")
	}

	_, span := StartAgentSpan(ctx, "openai/gpt-4o-mini")
	span.End()

	RecordTokens(ctx, "openai", "gpt-4o-mini", 100, 50)
	RecordToolOutcome(ctx, "fs_read", true, 5*time.Millisecond)
	RecordRetry(ctx, "openai", 1)
	RecordFallback(ctx, "openai", "anthropic")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sd(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned unexpected error: %v", err)
	}
	if atomic.LoadInt64(hits) == 0 {
		t.Fatal("fake OTLP collector did not receive any export, expected at least 1")
	}
}

func TestStartSpans_ReturnsValidSpan(t *testing.T) {
	t.Parallel()

	_, sd, err := setupForSpanTests(t)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	defer func() {
		if err := sd(context.Background()); err != nil {
			t.Logf("shutdown error: %v", err)
		}
	}()

	ctx := context.Background()
	if _, sp := StartAgentSpan(ctx, "model"); !sp.SpanContext().IsValid() {
		t.Fatal("StartAgentSpan returned invalid span context")
	}
	if _, sp := StartLLMSpan(ctx, "openai", "gpt"); !sp.SpanContext().IsValid() {
		t.Fatal("StartLLMSpan returned invalid span context")
	}
	if _, sp := StartToolSpan(ctx, "fs_read", "call-1"); !sp.SpanContext().IsValid() {
		t.Fatal("StartToolSpan returned invalid span context")
	}
}

// TestRecord_NoOpBeforeInit resetGlobal() でパッケージグローバルを操作するため、
// 明示的に t.Parallel() を付けない。他のテストとシリアル実行を保証することで
// global 状態を競合させずに「Init していない状態でも panic しない」ことを検証する
func TestRecord_NoOpBeforeInit(t *testing.T) {
	resetGlobal()
	ctx := context.Background()
	RecordTokens(ctx, "openai", "gpt", 1, 1)
	RecordToolOutcome(ctx, "fs_read", false, time.Millisecond)
	RecordRetry(ctx, "openai", 2)
	RecordFallback(ctx, "openai", "anthropic")
}

func TestStripScheme(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"http://127.0.0.1:4318", "127.0.0.1:4318"},
		{"https://otlp.example.com", "otlp.example.com"},
		{"otlp.example.com:4318", "otlp.example.com:4318"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripScheme(c.in); got != c.want {
			t.Errorf("stripScheme(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRecordTokens_ZeroSkipsCounters(t *testing.T) {
	endpoint, _, cleanup := fakeOTLPCollector(t)
	defer cleanup()
	sd, err := InitTelemetry(context.Background(), TelemetryConfig{
		Enabled:                true,
		Endpoint:               endpoint,
		Insecure:               true,
		SampleRatio:            0, // 0 から 1.0 へのフォールバック分岐
		ServiceName:            "",
		MetricsIntervalSeconds: 0,
	}, newDiscardLogger())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer func() { _ = sd(context.Background()) }()

	// in=0 と out=0 はそれぞれ Add を呼ばないこと（panic 等が起きなければ良い）
	RecordTokens(context.Background(), "openai", "gpt", 0, 0)
	RecordTokens(context.Background(), "openai", "gpt", 5, 0)
	RecordTokens(context.Background(), "openai", "gpt", 0, 5)
}

func TestRecordToolOutcome_FailurePath(t *testing.T) {
	endpoint, _, cleanup := fakeOTLPCollector(t)
	defer cleanup()
	sd, err := InitTelemetry(context.Background(), TelemetryConfig{
		Enabled:     true,
		Endpoint:    endpoint,
		Insecure:    true,
		SampleRatio: 1.0,
		ServiceName: "test-fail",
	}, newDiscardLogger())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer func() { _ = sd(context.Background()) }()

	RecordToolOutcome(context.Background(), "shell", false, 10*time.Millisecond)
	RecordToolOutcome(context.Background(), "shell", true, 10*time.Millisecond)
}

func TestNoopShutdown_AlwaysSucceeds(t *testing.T) {
	t.Parallel()
	sd := noopShutdown()
	if err := sd(context.Background()); err != nil {
		t.Fatalf("noopShutdown returned error: %v", err)
	}
}

// setupForSpanTests fake collector を起動して InitTelemetry を呼び、Shutdown を返す
func setupForSpanTests(t *testing.T) (string, Shutdown, error) {
	t.Helper()
	endpoint, _, cleanup := fakeOTLPCollector(t)
	t.Cleanup(cleanup)
	sd, err := InitTelemetry(context.Background(), TelemetryConfig{
		Enabled:                true,
		Endpoint:               endpoint,
		Insecure:               true,
		SampleRatio:            1.0,
		ServiceName:            "test-svc",
		MetricsIntervalSeconds: 1,
	}, newDiscardLogger())
	return endpoint, sd, err
}

// resetGlobal テスト間で global instruments をクリアする内部ヘルパ
// globalMu を取って globalInstruments() / InitTelemetry との race を避ける
// 旧実装の otel.SetTracerProvider(otel.GetTracerProvider()) は前回の TracerProvider を
// 上書きせず無効化にならなかったため、明示的に no-op の Tracer/Meter Provider を入れ直す
func resetGlobal() {
	globalMu.Lock()
	global = nil
	globalMu.Unlock()
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	otel.SetMeterProvider(metricnoop.NewMeterProvider())
}
