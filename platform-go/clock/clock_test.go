package clock_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/an-vt/ecom/platform-go/clock"
)

func TestSystemIsUTCAndMonotonicEnough(t *testing.T) {
	before := time.Now().Add(-time.Second)
	got := clock.System{}.Now()
	assert.Equal(t, time.UTC, got.Location())
	assert.True(t, got.After(before))
}

func TestFixed(t *testing.T) {
	start := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	c := clock.NewFixed(start)
	assert.Equal(t, start, c.Now())
	c.Advance(90 * time.Second)
	assert.Equal(t, start.Add(90*time.Second), c.Now())
	later := start.Add(24 * time.Hour)
	c.Set(later)
	assert.Equal(t, later, c.Now())
	var _ clock.Clock = c
}
