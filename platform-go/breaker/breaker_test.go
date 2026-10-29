package breaker_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/errs"
)

type recorder struct {
	mu      sync.Mutex
	states  []breaker.State
	results map[string]int
}

func newRecorder() *recorder { return &recorder{results: map[string]int{}} }
func (r *recorder) StateChanged(_, _ string, to breaker.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, to)
}
func (r *recorder) Result(_, _, res string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[res]++
}
func (r *recorder) count(res string) int { r.mu.Lock(); defer r.mu.Unlock(); return r.results[res] }

var (
	errDown = errors.New("unavailable")
	errBiz  = errs.NotFound("NOT_FOUND", "business error")
)

func isInfra(err error) bool { return errors.Is(err, errDown) }

func fail(b *breaker.Breaker, n int, err error) {
	for i := 0; i < n; i++ {
		_, _ = breaker.Call(b, func() (int, error) { return 0, err })
	}
}

func newReg(rec breaker.Observer, reset time.Duration) *breaker.Registry {
	return breaker.NewRegistry(breaker.Settings{
		ErrorRatio: 0.5, MinRequests: 4, Window: time.Minute, ResetTimeout: reset, MaxHalfOpen: 1,
		IsFailure: isInfra, Observer: rec,
	})
}

func TestOpensAfterEnoughInfrastructureFailures(t *testing.T) {
	rec := newRecorder()
	b := newReg(rec, time.Minute).Get("catalog", "GetSku")

	fail(b, 3, errDown)
	assert.Equal(t, breaker.Closed, b.State(), "below MinRequests the circuit stays closed")
	fail(b, 1, errDown)
	assert.Equal(t, breaker.Open, b.State())

	called := false
	_, err := breaker.Call(b, func() (int, error) { called = true; return 1, nil })
	require.Error(t, err)
	assert.False(t, called, "an open circuit must not call the dependency")

	var oe *breaker.OpenError
	require.ErrorAs(t, err, &oe)
	assert.Equal(t, "catalog", oe.Target)
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err), "maps to HTTP 503")
	assert.Equal(t, breaker.CodeOpen, errs.CodeOf(err))
	d, ok := breaker.RetryAfter(err)
	assert.True(t, ok)
	assert.Equal(t, time.Minute, d)
	assert.Contains(t, err.Error(), "catalog/GetSku")

	assert.Equal(t, 4, rec.count(breaker.ResultFailure))
	assert.Equal(t, 1, rec.count(breaker.ResultReject))
	assert.Equal(t, []breaker.State{breaker.Closed, breaker.Open}, rec.states)
}

func TestBusinessErrorsNeverOpenTheCircuit(t *testing.T) {
	b := newReg(nil, time.Minute).Get("catalog", "GetSku")
	fail(b, 50, errBiz)
	assert.Equal(t, breaker.Closed, b.State())
	_, err := breaker.Call(b, func() (int, error) { return 0, errBiz })
	assert.ErrorIs(t, err, errBiz, "the business error still reaches the caller unchanged")
}

func TestHalfOpenRecoversOnSuccess(t *testing.T) {
	b := newReg(nil, 40*time.Millisecond).Get("inventory", "CheckStock")
	fail(b, 4, errDown)
	require.Equal(t, breaker.Open, b.State())

	require.Eventually(t, func() bool { return b.State() == breaker.HalfOpen }, 2*time.Second, 10*time.Millisecond)
	got, err := breaker.Call(b, func() (string, error) { return "ok", nil })
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
	assert.Equal(t, breaker.Closed, b.State())
}

func TestHalfOpenFailureReopens(t *testing.T) {
	b := newReg(nil, 40*time.Millisecond).Get("inventory", "CheckStock")
	fail(b, 4, errDown)
	require.Eventually(t, func() bool { return b.State() == breaker.HalfOpen }, 2*time.Second, 10*time.Millisecond)
	fail(b, 1, errDown)
	assert.Equal(t, breaker.Open, b.State())
}

func TestBreakersAreIndependentPerTargetAndMethod(t *testing.T) {
	reg := newReg(nil, time.Minute)
	a, b := reg.Get("catalog", "GetSku"), reg.Get("catalog", "GetProductsByIds")
	assert.Same(t, a, reg.Get("catalog", "GetSku"), "cached")
	fail(a, 4, errDown)
	assert.Equal(t, breaker.Open, a.State())
	assert.Equal(t, breaker.Closed, b.State())
	states := reg.States()
	assert.Equal(t, breaker.Open, states["catalog/GetSku"])
	assert.Equal(t, breaker.Closed, states["catalog/GetProductsByIds"])
}

func TestDefaultsAndNoRetryAfterForOtherErrors(t *testing.T) {
	reg := breaker.NewRegistry(breaker.Settings{})
	b := reg.Get("x", "y")
	_, err := breaker.Call(b, func() (int, error) { return 0, errDown })
	assert.ErrorIs(t, err, errDown)
	assert.Equal(t, breaker.Closed, b.State())
	_, ok := breaker.RetryAfter(errDown)
	assert.False(t, ok)

	d := breaker.DefaultSettings()
	assert.Equal(t, 0.5, d.ErrorRatio)
	assert.Equal(t, uint32(10), d.MinRequests)
	assert.Equal(t, 30*time.Second, d.ResetTimeout)
}

func TestTypedCallReturnsZeroOnError(t *testing.T) {
	b := newReg(nil, time.Minute).Get("a", "b")
	v, err := breaker.Call(b, func() (int, error) { return 7, errDown })
	assert.Error(t, err)
	assert.Equal(t, 0, v)
	v, err = breaker.Call(b, func() (int, error) { return 7, nil })
	assert.NoError(t, err)
	assert.Equal(t, 7, v)
}

func TestStateStrings(t *testing.T) {
	assert.Equal(t, "closed", breaker.Closed.String())
	assert.Equal(t, "half-open", breaker.HalfOpen.String())
	assert.Equal(t, "open", breaker.Open.String())
}

func TestConcurrentUseIsSafe(t *testing.T) {
	reg := newReg(newRecorder(), time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := reg.Get("svc", "m")
			_, _ = breaker.Call(b, func() (int, error) { return 0, errDown })
		}()
	}
	wg.Wait()
	assert.Equal(t, breaker.Open, reg.Get("svc", "m").State())
}
