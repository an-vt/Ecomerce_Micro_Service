package app

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/order-service/internal/domain"
)

func TestSagaMetricsUseTheDocumentedSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newSagaMetrics(reg)
	m.Started()
	m.Started()
	m.Finished(domain.StateCompleted, "", 2*time.Second)
	m.Finished(domain.StateFailed, "CARD_DECLINED", time.Second)
	m.Finished(domain.StateFailed, "", time.Second)
	m.Compensation("ReleaseStock")
	m.StepTimeout(domain.StateProcessingPay)
	m.StaleReply()

	assert.Equal(t, 2.0, testutil.ToFloat64(m.started))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.completed))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.failed.WithLabelValues("CARD_DECLINED")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.failed.WithLabelValues("UNKNOWN")), "a failure always has a reason label")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.compensations.WithLabelValues("ReleaseStock")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.timeouts.WithLabelValues("PROCESSING_PAYMENT")))
	assert.Equal(t, 1.0, testutil.ToFloat64(m.stale))

	families, err := reg.Gather()
	require.NoError(t, err)
	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
	}
	for _, want := range []string{"saga_started_total", "saga_completed_total", "saga_failed_total", "saga_compensations_total",
		"saga_step_timeouts_total", "saga_stale_replies_total", "saga_duration_seconds"} {
		assert.Contains(t, strings.Join(names, " "), want)
	}
}
