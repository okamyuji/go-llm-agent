package obs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// tracerName Tracer と Meter で共有する計装名
const tracerName = "github.com/okamyuji/go-llm-agent"

// TelemetryConfig OTLP exporter とサンプリングの設定
type TelemetryConfig struct {
	Enabled                bool
	Endpoint               string
	Insecure               bool
	SampleRatio            float64
	ServiceName            string
	MetricsIntervalSeconds int
}

// Shutdown InitTelemetry が返すクリーンアップ関数の型
type Shutdown func(context.Context) error

// telemetryInstruments TracerProvider と MeterProvider と計測器を保持する
type telemetryInstruments struct {
	tokenInput    metric.Int64Counter
	tokenOutput   metric.Int64Counter
	toolDuration  metric.Float64Histogram
	toolSuccess   metric.Int64Counter
	toolFailure   metric.Int64Counter
	retryCounter  metric.Int64Counter
	fallbackTotal metric.Int64Counter
}

// global 全体で 1 つだけ保持する計測器セット
// 並行する InitTelemetry 呼び出しでも RWMutex で安全に書き換える
var (
	global   *telemetryInstruments
	globalMu sync.RWMutex
)

// noopShutdown 何もしないシャットダウン関数を返す
func noopShutdown() Shutdown {
	return func(context.Context) error { return nil }
}

// InitTelemetry TelemetryConfig.Enabled が false のとき no-op Shutdown を返す
// 有効化時は OTLP HTTP exporter と TracerProvider / MeterProvider を構築し
// otel.SetTracerProvider と otel.SetMeterProvider にセットする
func InitTelemetry(ctx context.Context, c TelemetryConfig, logger *slog.Logger) (Shutdown, error) {
	if !c.Enabled {
		if logger != nil {
			logger.Debug("telemetry disabled, returning noop shutdown")
		}
		return noopShutdown(), nil
	}

	serviceName := c.ServiceName
	if serviceName == "" {
		serviceName = "go-llm-agent"
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	traceOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(stripScheme(c.Endpoint))}
	if c.Insecure {
		traceOpts = append(traceOpts, otlptracehttp.WithInsecure())
	}
	traceExp, err := otlptrace.New(ctx, otlptracehttp.NewClient(traceOpts...))
	if err != nil {
		return nil, fmt.Errorf("otel trace exporter: %w", err)
	}

	sampleRatio := c.SampleRatio
	if sampleRatio <= 0 {
		sampleRatio = 1.0
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio))),
	)
	otel.SetTracerProvider(tp)

	metricInterval := time.Duration(c.MetricsIntervalSeconds) * time.Second
	if metricInterval <= 0 {
		metricInterval = 30 * time.Second
	}
	metricOpts := []otlpmetrichttp.Option{otlpmetrichttp.WithEndpoint(stripScheme(c.Endpoint))}
	if c.Insecure {
		metricOpts = append(metricOpts, otlpmetrichttp.WithInsecure())
	}
	metricExp, err := otlpmetrichttp.New(ctx, metricOpts...)
	if err != nil {
		// metric exporter 作成失敗時は既に走り出している TracerProvider と
		// trace exporter を一緒に shutdown して span flush goroutine の leak を防ぐ
		// 親 ctx は既に canceled の可能性があるため Background から独立した shutdown ctx を起こす
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var errs []error
		errs = append(errs, fmt.Errorf("otel metric exporter: %w", err))
		if e := tp.Shutdown(shutdownCtx); e != nil { //nolint:contextcheck // 独立した shutdown ctx を意図的に使う
			errs = append(errs, fmt.Errorf("tracer provider shutdown: %w", e))
		}
		if e := traceExp.Shutdown(shutdownCtx); e != nil { //nolint:contextcheck
			errs = append(errs, fmt.Errorf("trace exporter shutdown: %w", e))
		}
		return nil, errors.Join(errs...)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(metricInterval))),
	)
	otel.SetMeterProvider(mp)

	if err := installInstruments(mp); err != nil {
		// 既に作成済みの TracerProvider と MeterProvider を best-effort で閉じる
		// 親 ctx は既に canceled の可能性があるため Background から独立した shutdown ctx を起こす
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if shutErr := tp.Shutdown(shutdownCtx); shutErr != nil && logger != nil { //nolint:contextcheck
			logger.Warn("otel: tracer provider shutdown failed after install error", "err", shutErr)
		}
		if shutErr := mp.Shutdown(shutdownCtx); shutErr != nil && logger != nil { //nolint:contextcheck
			logger.Warn("otel: meter provider shutdown failed after install error", "err", shutErr)
		}
		return nil, err
	}

	if logger != nil {
		// Endpoint にクレデンシャル付き URL (user:pass@host) が含まれる可能性に備えて
		// net/url.URL.Redacted で機密部分を伏せたうえでログに記録する
		logger.Info("telemetry enabled",
			"endpoint", redactEndpoint(c.Endpoint),
			"service", serviceName,
			"sample_ratio", sampleRatio,
		)
	}

	// shutdown 呼び出し側 (main の shutdown フック等) が canceled ctx を渡してきても
	// span/metric の最終 flush が確実に走るよう、Background ベースの内部 ctx を起こす
	// 入力 shutdownCtx は将来用に残すが、内部ロジックでは使わない設計とする
	shutdown := func(shutdownCtx context.Context) error {
		_ = shutdownCtx
		ctxBG, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var errs []error
		if err := tp.Shutdown(ctxBG); err != nil { //nolint:contextcheck
			errs = append(errs, fmt.Errorf("tracer provider shutdown: %w", err))
		}
		if err := mp.Shutdown(ctxBG); err != nil { //nolint:contextcheck
			errs = append(errs, fmt.Errorf("meter provider shutdown: %w", err))
		}
		return errors.Join(errs...)
	}
	return shutdown, nil
}

// stripScheme OTLP HTTP exporter の WithEndpoint が host:port のみを期待するため scheme を除去する
// 大文字小文字を問わず "http://" / "https://" を除く ("HTTPS://..." なども正しく扱う)
func stripScheme(endpoint string) string {
	lower := strings.ToLower(endpoint)
	for _, prefix := range []string{"http://", "https://"} {
		if strings.HasPrefix(lower, prefix) {
			return endpoint[len(prefix):]
		}
	}
	return endpoint
}

// redactEndpoint Endpoint URL に user:password が含まれていればマスクして返す
// URL でない場合 (host:port のみなど) はそのまま返す
func redactEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || u.Scheme == "" {
		return endpoint
	}
	return u.Redacted()
}

// installInstruments MeterProvider から共通の計測器セットを構築し global に格納する
func installInstruments(mp metric.MeterProvider) error {
	meter := mp.Meter(tracerName)
	tokenInput, err := meter.Int64Counter("llm.tokens.input")
	if err != nil {
		return fmt.Errorf("create tokens.input counter: %w", err)
	}
	tokenOutput, err := meter.Int64Counter("llm.tokens.output")
	if err != nil {
		return fmt.Errorf("create tokens.output counter: %w", err)
	}
	toolDuration, err := meter.Float64Histogram("tool.duration_ms")
	if err != nil {
		return fmt.Errorf("create tool duration histogram: %w", err)
	}
	toolSuccess, err := meter.Int64Counter("tool.success")
	if err != nil {
		return fmt.Errorf("create tool success counter: %w", err)
	}
	toolFailure, err := meter.Int64Counter("tool.failure")
	if err != nil {
		return fmt.Errorf("create tool failure counter: %w", err)
	}
	retryCounter, err := meter.Int64Counter("llm.retry.attempts")
	if err != nil {
		return fmt.Errorf("create retry counter: %w", err)
	}
	fallbackTotal, err := meter.Int64Counter("llm.fallback.total")
	if err != nil {
		return fmt.Errorf("create fallback counter: %w", err)
	}
	globalMu.Lock()
	global = &telemetryInstruments{
		tokenInput:    tokenInput,
		tokenOutput:   tokenOutput,
		toolDuration:  toolDuration,
		toolSuccess:   toolSuccess,
		toolFailure:   toolFailure,
		retryCounter:  retryCounter,
		fallbackTotal: fallbackTotal,
	}
	globalMu.Unlock()
	return nil
}

// globalInstruments 計測器セットを安全に読み出す
func globalInstruments() *telemetryInstruments {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// Tracer 計装ライブラリ共通の Tracer を返す。未初期化時は no-op
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// StartAgentSpan エージェント実行ルートのスパンを開始する
func StartAgentSpan(ctx context.Context, model string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "agent.run", trace.WithAttributes(
		attribute.String("agent.model", model),
	))
}

// StartLLMSpan LLM プロバイダー呼び出しスパンを開始する
func StartLLMSpan(ctx context.Context, providerName, model string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "llm.call", trace.WithAttributes(
		attribute.String("llm.provider", providerName),
		attribute.String("llm.model", model),
	))
}

// StartToolSpan ツール実行スパンを開始する
func StartToolSpan(ctx context.Context, toolName, callID string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "tool.execute", trace.WithAttributes(
		attribute.String("tool.name", toolName),
		attribute.String("tool.call_id", callID),
	))
}

// RecordTokens 入出力トークン数のカウンタを進める
func RecordTokens(ctx context.Context, providerName, model string, in, out int) {
	g := globalInstruments()
	if g == nil {
		return
	}
	attrs := metric.WithAttributes(
		attribute.String("llm.provider", providerName),
		attribute.String("llm.model", model),
	)
	if in > 0 {
		g.tokenInput.Add(ctx, int64(in), attrs)
	}
	if out > 0 {
		g.tokenOutput.Add(ctx, int64(out), attrs)
	}
}

// RecordToolOutcome ツール実行の成否とレイテンシを記録する
func RecordToolOutcome(ctx context.Context, toolName string, ok bool, latency time.Duration) {
	g := globalInstruments()
	if g == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("tool.name", toolName))
	g.toolDuration.Record(ctx, float64(latency.Milliseconds()), attrs)
	if ok {
		g.toolSuccess.Add(ctx, 1, attrs)
	} else {
		g.toolFailure.Add(ctx, 1, attrs)
	}
}

// RecordRetry LLM 呼び出しのリトライ試行数を計上する
func RecordRetry(ctx context.Context, providerName string, attempt int) {
	g := globalInstruments()
	if g == nil {
		return
	}
	g.retryCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("llm.provider", providerName),
		attribute.Int("llm.retry.attempt", attempt),
	))
}

// RecordFallback プロバイダーフォールバック発火を計上する
func RecordFallback(ctx context.Context, fromProvider, toProvider string) {
	g := globalInstruments()
	if g == nil {
		return
	}
	g.fallbackTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("llm.fallback.from", fromProvider),
		attribute.String("llm.fallback.to", toProvider),
	))
}
