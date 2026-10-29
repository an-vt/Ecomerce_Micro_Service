// Package otelx sets up OpenTelemetry tracing and propagates trace context through Kafka headers
// (docs/14-observability.md §3).
package otelx

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Config for Init.
type Config struct {
	Service  string
	Env      string
	Endpoint string // host:port or http://host:port of the OTLP gRPC receiver; empty disables export
}

// Init installs the W3C propagator and, when an endpoint is configured, a batching OTLP exporter.
// The returned function flushes and stops the provider.
func Init(ctx context.Context, c Config) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if c.Endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(normalise(c.Endpoint)), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}
	res := resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(c.Service), attribute.String("deployment.environment", c.Env))
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(2*time.Second)), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

func normalise(ep string) string {
	if len(ep) >= 7 && (ep[:7] == "http://" || (len(ep) >= 8 && ep[:8] == "https://")) {
		return ep
	}
	return "http://" + ep
}

// untraced is a valid but unsampled remote span context: with the default parent-based sampler, every span started
// under it is dropped instead of exported. Its ids are a recognisable constant so IsUntraced can tell it apart from a
// real trace.
var untraced = trace.NewSpanContext(trace.SpanContextConfig{
	TraceID: trace.TraceID{0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef},
	SpanID:  trace.SpanID{0xde, 0xad, 0xbe, 0xef, 0xde, 0xad, 0xbe, 0xef},
	Remote:  true, // TraceFlags 0: not sampled
})

// Untraced returns ctx under which nothing is traced. Use it for work nobody wants a trace of: health checks and the
// polling loops of the outbox relay, which would otherwise create hundreds of one-span root traces a minute and bury
// the traces that matter. Work that continues a request (a message with a stored traceparent) re-establishes its own
// parent with Extract and is unaffected.
func Untraced(ctx context.Context) context.Context {
	return trace.ContextWithRemoteSpanContext(ctx, untraced)
}

// IsUntraced reports whether sc is the placeholder of Untraced (loggers use it to avoid printing a made-up trace id).
func IsUntraced(sc trace.SpanContext) bool { return sc.TraceID() == untraced.TraceID() }

// Tracer returns the named tracer.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// HeaderCarrier adapts a header map (Kafka headers, outbox row headers) to a propagation carrier.
type HeaderCarrier map[string]string

func (c HeaderCarrier) Get(k string) string { return c[k] }
func (c HeaderCarrier) Set(k, v string)     { c[k] = v }
func (c HeaderCarrier) Keys() []string {
	ks := make([]string, 0, len(c))
	for k := range c {
		ks = append(ks, k)
	}
	return ks
}

// Inject writes the trace context of ctx into a new header map (traceparent, tracestate).
func Inject(ctx context.Context) map[string]string {
	h := HeaderCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, h)
	return h
}

// Extract returns ctx carrying the trace context found in headers.
func Extract(ctx context.Context, headers map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, HeaderCarrier(headers))
}
