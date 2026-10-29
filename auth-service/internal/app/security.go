package app

import "github.com/prometheus/client_golang/prometheus"

// securityMetrics implements usecase.SecurityEvents with the Prometheus series of docs/14.
type securityMetrics struct{ reuse prometheus.Counter }

func newSecurityMetrics(reg prometheus.Registerer) *securityMetrics {
	m := &securityMetrics{reuse: prometheus.NewCounter(prometheus.CounterOpts{
		Name: "auth_refresh_reuse_detected_total", Help: "Refresh tokens presented again after they were rotated away (stolen token or stale client)."})}
	reg.MustRegister(m.reuse)
	return m
}

func (m *securityMetrics) RefreshReuseDetected() { m.reuse.Inc() }
