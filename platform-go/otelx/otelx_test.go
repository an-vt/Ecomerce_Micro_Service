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

func TestUntracedDropsEverythingStartedUnderIt(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())))
	otel.SetTracerProvider(tp)

	// a normal root span is sampled...
	_, root := otelx.Tracer("t").Start(context.Background(), "root")
	assert.True(t, root.SpanContext().IsSampled())
	root.End()

	// ...but the same work under Untraced is not: it is what keeps health checks and polling out of Tempo
	ctx := otelx.Untraced(context.Background())
	_, quiet := otelx.Tracer("t").Start(ctx, "poll")
	defer quiet.End()
	assert.False(t, quiet.SpanContext().IsSampled())
	assert.True(t, otelx.IsUntraced(trace.SpanContextFromContext(ctx)))
	assert.False(t, otelx.IsUntraced(root.SpanContext()), "a real trace is never mistaken for the placeholder")
}

func TestAMessageWithAStoredTraceparentIsTracedEvenInAnUntracedLoop(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())))
	otel.SetTracerProvider(tp)
	requestCtx, request := otelx.Tracer("t").Start(context.Background(), "request")
	defer request.End()
	stored := otelx.Inject(requestCtx) // what the outbox row keeps

	relayCtx := otelx.Untraced(context.Background()) // the polling loop
	publishCtx := otelx.Extract(relayCtx, stored)    // what the producer does for each message
	_, publish := otelx.Tracer("t").Start(publishCtx, "publish")
	defer publish.End()
	assert.True(t, publish.SpanContext().IsSampled(), "the saga step stays in the request's trace")
	assert.Equal(t, request.SpanContext().TraceID(), publish.SpanContext().TraceID())

	_, bare := otelx.Tracer("t").Start(otelx.Extract(relayCtx, map[string]string{}), "publish")
	defer bare.End()
	assert.False(t, bare.SpanContext().IsSampled(), "a message with no trace is not given a fresh one")
}
