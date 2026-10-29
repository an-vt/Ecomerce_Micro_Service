package metrics_test

import (
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/metrics"
)

func TestHandlerExposesSharedMetrics(t *testing.T) {
	m := metrics.New()
	m.KafkaConsumed.WithLabelValues("t", "ok").Inc()
	m.OutboxPending.Set(3)
	m.GRPCServer.WithLabelValues("svc", "M", "OK").Observe(0.01)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, err := io.ReadAll(rec.Result().Body)
	require.NoError(t, err)
	for _, want := range []string{"go_goroutines", "kafka_messages_consumed_total", "outbox_pending 3", "grpc_server_duration_seconds_bucket"} {
		assert.Contains(t, string(body), want)
	}
}

func TestBreakerObserverFeedsGaugesAndCounters(t *testing.T) {
	m := metrics.New()
	reg := breaker.NewRegistry(breaker.Settings{MinRequests: 2, ErrorRatio: 0.5, ResetTimeout: time.Minute, Observer: m.BreakerObserver()})
	b := reg.Get("catalog", "GetSku")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.BreakerState.WithLabelValues("catalog", "GetSku")))

	boom := assert.AnError
	for i := 0; i < 2; i++ {
		_, _ = breaker.Call(b, func() (int, error) { return 0, boom })
	}
	_, _ = breaker.Call(b, func() (int, error) { return 0, nil }) // rejected
	assert.Equal(t, 2.0, testutil.ToFloat64(m.BreakerState.WithLabelValues("catalog", "GetSku")))
	assert.Equal(t, 2.0, testutil.ToFloat64(m.BreakerEvents.WithLabelValues("catalog", "GetSku", breaker.ResultFailure)))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.BreakerEvents.WithLabelValues("catalog", "GetSku", breaker.ResultReject)))
}
