package retry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/retry"
)

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	one := func() float64 { return 0.999999999 } // ~ceiling
	base, max := 100*time.Millisecond, 1*time.Second
	assert.InDelta(t, 100e6, float64(retry.Backoff(1, base, max, one)), 1e3)
	assert.InDelta(t, 200e6, float64(retry.Backoff(2, base, max, one)), 1e3)
	assert.InDelta(t, 400e6, float64(retry.Backoff(3, base, max, one)), 1e3)
	assert.InDelta(t, 1e9, float64(retry.Backoff(10, base, max, one)), 1e3, "capped at max")
	assert.Equal(t, time.Duration(0), retry.Backoff(3, base, max, func() float64 { return 0 }))
	assert.InDelta(t, 100e6, float64(retry.Backoff(0, base, max, one)), 1e3, "attempt < 1 behaves like 1")
	d := retry.Backoff(2, base, max, nil) // real jitter stays within the ceiling
	assert.True(t, d >= 0 && d < 200*time.Millisecond)
}

func fakeSleep(slept *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error { *slept = append(*slept, d); return nil }
}

func TestDoSucceedsAfterRetries(t *testing.T) {
	var slept []time.Duration
	calls := 0
	err := retry.Do(context.Background(), retry.Policy{MaxAttempts: 4, Sleep: fakeSleep(&slept), Jitter: func() float64 { return 0.5 }},
		func(context.Context) error {
			calls++
			if calls < 3 {
				return errors.New("transient")
			}
			return nil
		})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
	assert.Equal(t, []time.Duration{50 * time.Millisecond, 100 * time.Millisecond}, slept)
}

func TestDoGivesUpAfterMaxAttempts(t *testing.T) {
	var slept []time.Duration
	calls := 0
	boom := errors.New("boom")
	err := retry.Do(context.Background(), retry.Policy{Sleep: fakeSleep(&slept)}, func(context.Context) error { calls++; return boom })
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 3, calls, "default MaxAttempts")
	assert.Len(t, slept, 2, "no sleep after the last attempt")
}

func TestDoStopsOnNonRetryable(t *testing.T) {
	calls := 0
	fatal := errors.New("fatal")
	err := retry.Do(context.Background(), retry.Policy{Retryable: func(e error) bool { return !errors.Is(e, fatal) }, Sleep: fakeSleep(new([]time.Duration))},
		func(context.Context) error { calls++; return fatal })
	assert.ErrorIs(t, err, fatal)
	assert.Equal(t, 1, calls)
}

func TestDoReturnsSleepError(t *testing.T) {
	stop := errors.New("interrupted")
	err := retry.Do(context.Background(), retry.Policy{Sleep: func(context.Context, time.Duration) error { return stop }},
		func(context.Context) error { return errors.New("x") })
	assert.ErrorIs(t, err, stop)
}

func TestDefaultSleepHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retry.Do(ctx, retry.Policy{Base: time.Hour, Max: time.Hour, Jitter: func() float64 { return 0.99 }},
		func(context.Context) error { calls++; return errors.New("x") })
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)
}

func TestDefaultSleepWaits(t *testing.T) {
	calls := 0
	start := time.Now()
	err := retry.Do(context.Background(), retry.Policy{MaxAttempts: 2, Base: 20 * time.Millisecond, Jitter: func() float64 { return 0.99 }},
		func(context.Context) error { calls++; return errors.New("x") })
	assert.Error(t, err)
	assert.Equal(t, 2, calls)
	assert.GreaterOrEqual(t, time.Since(start), 10*time.Millisecond)
}
