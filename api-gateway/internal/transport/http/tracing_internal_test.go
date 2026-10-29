package http

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/an-vt/ecom/platform-go/otelx"
)

// tracedEngine is a gateway-shaped engine with a recording tracer; handlers report the span context they see.
func tracedEngine(t *testing.T) (*gin.Engine, *tracetest.InMemoryExporter, *trace.SpanContext) {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample()))))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	seen := new(trace.SpanContext)
	r := gin.New()
	r.Use(tracing())
	handler := func(status int) gin.HandlerFunc {
		return func(c *gin.Context) {
			*seen = trace.SpanContextFromContext(c.Request.Context())
			c.Status(status)
		}
	}
	r.GET("/v1/api/orders/:orderId", handler(200))
	r.GET("/v1/api/boom", handler(503))
	r.GET("/health/ready", handler(200))
	r.GET("/metrics", handler(200))
	r.GET("/docs", handler(200))
	return r, exp, seen
}

func TestEachRequestRootsATraceTheClientCanQuote(t *testing.T) {
	r, exp, seen := tracedEngine(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/v1/api/orders/o-1?secret=token-in-query", nil))

	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	s := spans[0]
	assert.Equal(t, "GET /v1/api/orders/:orderId", s.Name, "named after the route, not the path: one series, not one per order")
	assert.Equal(t, trace.SpanKindServer, s.SpanKind)
	assert.False(t, s.Parent.IsValid(), "a root span")
	attrs := map[string]any{}
	for _, a := range s.Attributes {
		attrs[string(a.Key)] = a.Value.AsInterface()
	}
	assert.Equal(t, "/v1/api/orders/:orderId", attrs["http.route"])
	assert.EqualValues(t, 200, attrs["http.response.status_code"])
	for k, v := range attrs {
		if str, ok := v.(string); ok {
			assert.NotContains(t, str, "token-in-query", "the query string (which can hold a token) is never recorded: %s", k)
		}
	}

	assert.Equal(t, s.SpanContext.TraceID().String(), w.Header().Get(TraceHeader), "the response carries the trace id")
	assert.Equal(t, s.SpanContext, *seen, "the handler, and so the gRPC calls and the access log, run inside the span")
}

func TestServerErrorsMarkTheSpanFailed(t *testing.T) {
	r, exp, _ := tracedEngine(t)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/api/boom", nil))
	spans := exp.GetSpans()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status.Code)
	exp.Reset()
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/api/orders/x", nil))
	assert.Equal(t, codes.Unset, exp.GetSpans()[0].Status.Code, "4xx and 2xx are not failures of the gateway")
}

// The gateway is the public edge: a client must not be able to choose the trace (force sampling, graft onto another trace).
func TestAClientsTraceparentIsNotAdopted(t *testing.T) {
	r, exp, _ := tracedEngine(t)
	req := httptest.NewRequest("GET", "/v1/api/orders/o-1", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	r.ServeHTTP(httptest.NewRecorder(), req)
	s := exp.GetSpans()[0]
	assert.NotEqual(t, "4bf92f3577b34da6a3ce929d0e0e4736", s.SpanContext.TraceID().String())
	assert.False(t, s.Parent.IsValid())
}

func TestOperationalAndUnmatchedRoutesAreNotTraced(t *testing.T) {
	r, exp, seen := tracedEngine(t)
	for _, path := range []string{"/health/ready", "/metrics", "/docs", "/wp-login.php", "/v1/api/does-not-exist"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		assert.Empty(t, w.Header().Get(TraceHeader), path)
	}
	assert.Empty(t, exp.GetSpans(), "none of them reaches Tempo; scanners and health checks would drown the real traces")

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/health/ready", nil))
	assert.True(t, otelx.IsUntraced(*seen), "and work done for them (a Redis ping) is not traced either")
}
