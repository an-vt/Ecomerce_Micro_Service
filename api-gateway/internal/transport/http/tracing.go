package http

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/an-vt/ecom/platform-go/otelx"
)

// TraceHeader carries the trace id of a request back to the client, so a report of a slow or failing call can quote it.
const TraceHeader = "X-Trace-Id"

// tracing starts the server span that roots the trace of every API request: from here the trace follows the gRPC calls
// to the services and, through the outbox, the saga over Kafka (docs/14 §3). It also puts the span in the request
// context, so the access log carries its trace_id and Loki can be searched by it.
//
// Two choices worth knowing:
//   - The incoming traceparent is NOT adopted. The gateway is the public edge: honouring a client's trace context would let
//     anyone force sampling or graft requests onto someone else's trace. Every request starts a fresh trace.
//   - Operational routes (health, metrics, docs) and unmatched paths (scanners) are not traced at all; they would
//     drown the real traces in Tempo.
func tracing() gin.HandlerFunc {
	tracer := otel.Tracer("api-gateway")
	return func(c *gin.Context) {
		route := c.FullPath()
		if route == "" || isOperational(route) {
			c.Request = c.Request.WithContext(otelx.Untraced(c.Request.Context()))
			c.Next()
			return
		}
		ctx, span := tracer.Start(c.Request.Context(), c.Request.Method+" "+route,
			trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", c.Request.Method), attribute.String("http.route", route)))
		defer span.End()
		c.Request = c.Request.WithContext(ctx)
		c.Header(TraceHeader, span.SpanContext().TraceID().String())

		c.Next()

		status := c.Writer.Status()
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= 500 {
			span.SetStatus(codes.Error, strconv.Itoa(status))
		}
	}
}

func isOperational(route string) bool {
	return strings.HasPrefix(route, "/health") || route == "/metrics" || strings.HasPrefix(route, "/docs")
}
