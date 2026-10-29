package app

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestRefreshReuseIsCounted(t *testing.T) {
	m := newSecurityMetrics(prometheus.NewRegistry())
	m.RefreshReuseDetected()
	m.RefreshReuseDetected()
	assert.Equal(t, 2.0, testutil.ToFloat64(m.reuse))
}
