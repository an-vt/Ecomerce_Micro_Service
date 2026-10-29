package app

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/an-vt/ecom/order-service/internal/domain"
)

// sagaMetrics implements usecase.SagaMetrics with the Prometheus series of docs/14.
type sagaMetrics struct {
	started       prometheus.Counter
	completed     prometheus.Counter
	failed        *prometheus.CounterVec
	compensations *prometheus.CounterVec
	timeouts      *prometheus.CounterVec
	stale         prometheus.Counter
	duration      *prometheus.HistogramVec
}

func newSagaMetrics(reg prometheus.Registerer) *sagaMetrics {
	m := &sagaMetrics{
		started:       prometheus.NewCounter(prometheus.CounterOpts{Name: "saga_started_total", Help: "Checkout sagas started."}),
		completed:     prometheus.NewCounter(prometheus.CounterOpts{Name: "saga_completed_total", Help: "Checkout sagas that confirmed the order."}),
		failed:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "saga_failed_total", Help: "Checkout sagas that cancelled the order, by reason."}, []string{"reason"}),
		compensations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "saga_compensations_total", Help: "Compensation commands sent, by command."}, []string{"step"}),
		timeouts:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "saga_step_timeouts_total", Help: "Saga steps that missed their deadline, by step."}, []string{"step"}),
		stale:         prometheus.NewCounter(prometheus.CounterOpts{Name: "saga_stale_replies_total", Help: "Replies ignored because they no longer fit the saga state."}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "saga_duration_seconds", Help: "Time from the start of a checkout to its end.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}}, []string{"outcome"}),
	}
	reg.MustRegister(m.started, m.completed, m.failed, m.compensations, m.timeouts, m.stale, m.duration)
	return m
}

func (m *sagaMetrics) Started()    { m.started.Inc() }
func (m *sagaMetrics) StaleReply() { m.stale.Inc() }

func (m *sagaMetrics) Finished(state domain.SagaState, reason string, took time.Duration) {
	if state == domain.StateCompleted {
		m.completed.Inc()
		m.duration.WithLabelValues("completed").Observe(took.Seconds())
		return
	}
	if reason == "" {
		reason = "UNKNOWN"
	}
	m.failed.WithLabelValues(reason).Inc()
	m.duration.WithLabelValues("failed").Observe(took.Seconds())
}

func (m *sagaMetrics) Compensation(command string) { m.compensations.WithLabelValues(command).Inc() }
func (m *sagaMetrics) StepTimeout(step domain.SagaState) {
	m.timeouts.WithLabelValues(string(step)).Inc()
}
