package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestTraceContextHandler_AddsTraceIDWhenContextHasSpan(t *testing.T) {
	endpoint, _, cleanup := fakeOTLPCollector(t)
	defer cleanup()
	sd, err := InitTelemetry(context.Background(), TelemetryConfig{
		Enabled:                true,
		Endpoint:               endpoint,
		Insecure:               true,
		SampleRatio:            1.0,
		ServiceName:            "test-handler",
		MetricsIntervalSeconds: 1,
	}, newDiscardLogger())
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	defer func() { _ = sd(context.Background()) }()

	var buf bytes.Buffer
	logger := slog.New(NewTraceContextHandler(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, span := StartAgentSpan(context.Background(), "openai/gpt")
	defer span.End()
	logger.InfoContext(ctx, "in-span message", "k", "v")
	logger.Info("out-of-span message")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 log lines, got %d: %q", len(lines), buf.String())
	}

	var in, out map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &in); err != nil {
		t.Fatalf("unmarshal line 0: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &out); err != nil {
		t.Fatalf("unmarshal line 1: %v", err)
	}

	if _, ok := in["trace_id"]; !ok {
		t.Errorf("in-span log entry missing trace_id: %v", in)
	}
	if _, ok := in["span_id"]; !ok {
		t.Errorf("in-span log entry missing span_id: %v", in)
	}
	if _, ok := out["trace_id"]; ok {
		t.Errorf("out-of-span log entry should not have trace_id: %v", out)
	}
}

func TestTraceContextHandler_PassesThroughWhenContextHasNoSpan(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(NewTraceContextHandler(slog.NewJSONHandler(&buf, nil)))
	logger.Info("hello", "k", "v")

	if !strings.Contains(buf.String(), "\"msg\":\"hello\"") {
		t.Fatalf("unexpected log output: %q", buf.String())
	}
	if strings.Contains(buf.String(), "trace_id") {
		t.Fatalf("trace_id should not appear without a span: %q", buf.String())
	}
}

func TestTraceContextHandler_NilInnerIsSafe(t *testing.T) {
	t.Parallel()

	logger := slog.New(NewTraceContextHandler(nil))
	logger.Info("safe with nil inner")
}
