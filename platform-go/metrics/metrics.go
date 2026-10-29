// Package metrics owns the Prometheus registry and the metrics shared by all services (docs/14-observability.md §2).
// Service-specific metrics (saga_*, payments_*, …) are registered by the service on Metrics.Registry.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/an-vt/ecom/platform-go/breaker"
)

// LatencyBuckets suit service-to-service calls (seconds).
var LatencyBuckets = []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5}

// Metrics is the per-process metric set.
type Metrics struct {
	Registry *prometheus.Registry

	GRPCServer    *prometheus.HistogramVec // service, method, code
	GRPCClient    *prometheus.HistogramVec // target, method, code
	BreakerState  *prometheus.GaugeVec     // target, method (0 closed, 1 half-open, 2 open)
	BreakerEvents *prometheus.CounterVec   // target, method, event

	OutboxPending   prometheus.Gauge
	OutboxOldestAge prometheus.Gauge
	OutboxPublished *prometheus.CounterVec // topic
	OutboxErrors    *prometheus.CounterVec // topic

	KafkaConsumed *prometheus.CounterVec // topic, result: ok|duplicate|retry|dlq
	KafkaDLQ      *prometheus.CounterVec // topic
}

// New creates the registry with Go runtime and process collectors plus the shared metrics.
func New() *Metrics {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := &Metrics{
		Registry: r,
		GRPCServer: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "grpc_server_duration_seconds", Help: "gRPC server handling time.", Buckets: LatencyBuckets,
		}, []string{"service", "method", "code"}),
		GRPCClient: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "grpc_client_duration_seconds", Help: "gRPC client call time.", Buckets: LatencyBuckets,
		}, []string{"target", "method", "code"}),
		BreakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "circuit_breaker_state", Help: "0 closed, 1 half-open, 2 open.",
		}, []string{"target", "method"}),
		BreakerEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "circuit_breaker_events_total", Help: "Calls seen by the breaker by result.",
		}, []string{"target", "method", "event"}),
		OutboxPending:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "outbox_pending", Help: "Outbox rows waiting to be published."}),
		OutboxOldestAge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "outbox_oldest_age_seconds", Help: "Age of the oldest pending outbox row."}),
		OutboxPublished: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "outbox_published_total", Help: "Outbox rows published."}, []string{"topic"}),
		OutboxErrors:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "outbox_publish_errors_total", Help: "Outbox publish failures."}, []string{"topic"}),
		KafkaConsumed:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kafka_messages_consumed_total", Help: "Consumed messages by result."}, []string{"topic", "result"}),
		KafkaDLQ:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kafka_dlq_total", Help: "Messages sent to a dead-letter topic."}, []string{"topic"}),
	}
	r.MustRegister(m.GRPCServer, m.GRPCClient, m.BreakerState, m.BreakerEvents,
		m.OutboxPending, m.OutboxOldestAge, m.OutboxPublished, m.OutboxErrors, m.KafkaConsumed, m.KafkaDLQ)
	return m
}

// Handler serves the registry in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}

// BreakerObserver connects circuit breakers to the breaker metrics.
func (m *Metrics) BreakerObserver() breaker.Observer { return breakerObserver{m} }

type breakerObserver struct{ m *Metrics }

func (o breakerObserver) StateChanged(target, method string, to breaker.State) {
	o.m.BreakerState.WithLabelValues(target, method).Set(float64(to))
}

func (o breakerObserver) Result(target, method, result string) {
	o.m.BreakerEvents.WithLabelValues(target, method, result).Inc()
}
