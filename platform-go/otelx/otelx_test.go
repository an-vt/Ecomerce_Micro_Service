package otelx_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/an-vt/ecom/platform-go/otelx"
)

func TestInitWithoutEndpointStillPropagates(t *testing.T) {
	shutdown, err := otelx.Init(context.Background(), otelx.Config{Service: "t"})
	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))

	// install a recording provider to get a valid span context
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	ctx, span := otelx.Tracer("test").Start(context.Background(), "producer")
	defer span.End()

	headers := otelx.Inject(ctx)
	assert.Contains(t, headers, "traceparent")

	// a consumer in another process continues the same trace
	consumerCtx := otelx.Extract(context.Background(), headers)
	got := trace.SpanContextFromContext(consumerCtx)
	assert.True(t, got.IsValid())
	assert.True(t, got.IsRemote())
	assert.Equal(t, span.SpanContext().TraceID(), got.TraceID())
}

func TestInjectWithoutSpanIsEmpty(t *testing.T) {
	_, err := otelx.Init(context.Background(), otelx.Config{})
	require.NoError(t, err)
	assert.NotContains(t, otelx.Inject(context.Background()), "traceparent")
}

func TestInitWithEndpointCreatesProvider(t *testing.T) {
	shutdown, err := otelx.Init(context.Background(), otelx.Config{Service: "svc", Env: "test", Endpoint: "127.0.0.1:4317"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = shutdown(ctx) // nothing recorded; must not hang

	shutdown, err = otelx.Init(context.Background(), otelx.Config{Service: "svc", Endpoint: "https://collector:4317"})
	require.NoError(t, err)
	_ = shutdown(ctx)
}

func TestHeaderCarrier(t *testing.T) {
	c := otelx.HeaderCarrier{}
	c.Set("a", "1")
	assert.Equal(t, "1", c.Get("a"))
	assert.Equal(t, []string{"a"}, c.Keys())
}
