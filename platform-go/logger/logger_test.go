package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/otelx"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m), buf.String())
	return m
}

func TestJSONOutputWithServiceAndLevel(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Options{Service: "order-service", Level: "info", Writer: &buf})
	l.Debug("hidden")
	assert.Empty(t, buf.String(), "debug is below the level")
	l.Info("saga step completed", "order_id", "o1")
	m := decode(t, &buf)
	assert.Equal(t, "order-service", m["service"])
	assert.Equal(t, "saga step completed", m["msg"])
	assert.Equal(t, "o1", m["order_id"])
	assert.Equal(t, "INFO", m["level"])
}

func TestSecretsAreRedacted(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Options{Writer: &buf, Redact: []string{"card_number"}})
	l.Info("login", "password", "hunter2", "Authorization", "Bearer abc", "card_number", "4242", "email", "a@b.c",
		slog.Group("payment", slog.String("token", "tok_ok"), slog.String("method", "card")))
	m := decode(t, &buf)
	assert.Equal(t, "[REDACTED]", m["password"])
	assert.Equal(t, "[REDACTED]", m["Authorization"])
	assert.Equal(t, "[REDACTED]", m["card_number"])
	assert.Equal(t, "a@b.c", m["email"])
	pay := m["payment"].(map[string]any)
	assert.Equal(t, "[REDACTED]", pay["token"], "grouped keys are matched by their own name and by their dotted path")
	assert.Equal(t, "card", pay["method"])
}

func TestTraceAndRequestIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Options{Writer: &buf})

	tid, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	sid, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))
	ctx = logger.WithRequestID(ctx, "req-1")

	l.InfoContext(ctx, "hello")
	m := decode(t, &buf)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", m["trace_id"])
	assert.Equal(t, "00f067aa0ba902b7", m["span_id"])
	assert.Equal(t, "req-1", m["request_id"])

	buf.Reset()
	l.With("k", "v").WithGroup("g").InfoContext(ctx, "grouped")
	m = decode(t, &buf)
	assert.Equal(t, "v", m["k"], "With keeps the context handler")
	assert.Contains(t, m, "g")

	buf.Reset()
	l.Info("no ctx")
	m = decode(t, &buf)
	assert.NotContains(t, m, "trace_id")
	assert.NotContains(t, m, "request_id")
	assert.Equal(t, "", logger.RequestID(context.Background()))
}

func TestParseLevel(t *testing.T) {
	assert.Equal(t, slog.LevelDebug, logger.ParseLevel("DEBUG"))
	assert.Equal(t, slog.LevelWarn, logger.ParseLevel("warning"))
	assert.Equal(t, slog.LevelError, logger.ParseLevel(" error "))
	assert.Equal(t, slog.LevelInfo, logger.ParseLevel("nonsense"))
}

func TestDefaultWriterIsStdout(t *testing.T) {
	assert.NotNil(t, logger.New(logger.Options{Service: "x"}))
}

// Work done under otelx.Untraced is not traced, so printing its (made-up) trace id would send an operator looking for a
// trace that does not exist.
func TestUntracedWorkLogsNoTraceID(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(logger.Options{Writer: &buf})
	l.InfoContext(otelx.Untraced(context.Background()), "polling")
	assert.NotContains(t, decode(t, &buf), "trace_id")
}
