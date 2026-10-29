// Package breaker wraps sony/gobreaker (docs/09-resilience.md §1).
//
// A Registry keeps one breaker per (target, method), so a failing CatalogService.GetSku cannot trip
// CatalogService.GetProductsByIds and one failing upstream cannot trip another. Only errors that
// Settings.IsFailure classifies as failures count towards opening the circuit; business errors
// (NOT_FOUND, FAILED_PRECONDITION, …) are the dependency working correctly.
package breaker

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/an-vt/ecom/platform-go/errs"
)

// State is the circuit state; the numeric values are exported as the circuit_breaker_state gauge.
type State int

const (
	Closed   State = 0
	HalfOpen State = 1
	Open     State = 2
)

func (s State) String() string {
	switch s {
	case HalfOpen:
		return "half-open"
	case Open:
		return "open"
	}
	return "closed"
}

// Result of one call, as seen by the Observer.
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
	ResultReject  = "reject" // circuit open: the call was not made
)

// Observer receives breaker telemetry (metrics, logs). The zero value of Settings uses a no-op.
type Observer interface {
	StateChanged(target, method string, to State)
	Result(target, method, result string)
}

type nopObserver struct{}

func (nopObserver) StateChanged(string, string, State) {}
func (nopObserver) Result(string, string, string)      {}

// Settings configures every breaker of a Registry.
type Settings struct {
	ErrorRatio    float64       // open when failures/requests >= ratio (0..1)
	MinRequests   uint32        // ...and at least this many requests were seen in the window
	Window        time.Duration // closed-state counting window (gobreaker Interval)
	ResetTimeout  time.Duration // open -> half-open after this long (gobreaker Timeout)
	MaxHalfOpen   uint32        // trial calls allowed in half-open
	IsFailure     func(error) bool
	Observer      Observer
	OnOpenMessage string // optional text for OpenError
}

// DefaultSettings are the documented defaults.
func DefaultSettings() Settings {
	return Settings{ErrorRatio: 0.5, MinRequests: 10, Window: 10 * time.Second, ResetTimeout: 30 * time.Second, MaxHalfOpen: 1}
}

// CodeOpen is the error code of an open circuit.
const CodeOpen = errs.CodeUnavailable

// OpenError is returned instead of calling the dependency while the circuit is open. It is an
// errs.KindUnavailable error (HTTP 503) and carries the time after which a retry makes sense.
type OpenError struct {
	Target, Method string
	RetryAfter     time.Duration
	inner          *errs.Error
}

func (e *OpenError) Error() string {
	return fmt.Sprintf("circuit open for %s/%s (retry in %s): %v", e.Target, e.Method, e.RetryAfter.Round(time.Second), e.inner)
}

// Unwrap exposes the errs.Error so errs.KindOf / errs.CodeOf work on it.
func (e *OpenError) Unwrap() error { return e.inner }

// Registry creates and caches breakers.
type Registry struct {
	s  Settings
	mu sync.RWMutex
	m  map[string]*Breaker
}

// NewRegistry fills zero settings with defaults.
func NewRegistry(s Settings) *Registry {
	d := DefaultSettings()
	if s.ErrorRatio <= 0 || s.ErrorRatio > 1 {
		s.ErrorRatio = d.ErrorRatio
	}
	if s.MinRequests == 0 {
		s.MinRequests = d.MinRequests
	}
	if s.Window <= 0 {
		s.Window = d.Window
	}
	if s.ResetTimeout <= 0 {
		s.ResetTimeout = d.ResetTimeout
	}
	if s.MaxHalfOpen == 0 {
		s.MaxHalfOpen = d.MaxHalfOpen
	}
	if s.IsFailure == nil {
		s.IsFailure = func(err error) bool { return err != nil }
	}
	if s.Observer == nil {
		s.Observer = nopObserver{}
	}
	return &Registry{s: s, m: map[string]*Breaker{}}
}

// Get returns the breaker for (target, method), creating it on first use.
func (r *Registry) Get(target, method string) *Breaker {
	key := target + "/" + method
	r.mu.RLock()
	b, ok := r.m[key]
	r.mu.RUnlock()
	if ok {
		return b
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok = r.m[key]; ok {
		return b
	}
	b = r.newBreaker(target, method)
	r.m[key] = b
	return b
}

// States returns the current state of every breaker created so far, keyed "target/method".
func (r *Registry) States() map[string]State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]State, len(r.m))
	for k, b := range r.m {
		out[k] = b.State()
	}
	return out
}

// Breaker guards one (target, method).
type Breaker struct {
	target, method string
	cb             *gobreaker.CircuitBreaker[any]
	s              Settings
}

func (r *Registry) newBreaker(target, method string) *Breaker {
	s := r.s
	b := &Breaker{target: target, method: method, s: s}
	b.cb = gobreaker.NewCircuitBreaker[any](gobreaker.Settings{
		Name:        target + "/" + method,
		MaxRequests: s.MaxHalfOpen,
		Interval:    s.Window,
		Timeout:     s.ResetTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.Requests >= s.MinRequests && float64(c.TotalFailures)/float64(c.Requests) >= s.ErrorRatio
		},
		IsSuccessful: func(err error) bool { return err == nil || !s.IsFailure(err) },
		OnStateChange: func(_ string, _ gobreaker.State, to gobreaker.State) {
			s.Observer.StateChanged(target, method, fromGobreaker(to))
		},
	})
	s.Observer.StateChanged(target, method, Closed) // publish the initial gauge value
	return b
}

func fromGobreaker(s gobreaker.State) State {
	switch s {
	case gobreaker.StateHalfOpen:
		return HalfOpen
	case gobreaker.StateOpen:
		return Open
	}
	return Closed
}

// State returns the current state.
func (b *Breaker) State() State { return fromGobreaker(b.cb.State()) }

// Execute runs fn through the breaker. While the circuit is open (or the half-open trial quota is used) it
// returns *OpenError without calling fn. Errors from fn are returned unchanged.
func (b *Breaker) Execute(fn func() (any, error)) (any, error) {
	v, err := b.cb.Execute(fn)
	switch {
	case errors.Is(err, gobreaker.ErrOpenState), errors.Is(err, gobreaker.ErrTooManyRequests):
		b.s.Observer.Result(b.target, b.method, ResultReject)
		return nil, &OpenError{
			Target: b.target, Method: b.method, RetryAfter: b.s.ResetTimeout,
			inner: errs.Unavailable(CodeOpen, "upstream "+b.target+" is unavailable"),
		}
	case err != nil && b.s.IsFailure(err):
		b.s.Observer.Result(b.target, b.method, ResultFailure)
	default:
		b.s.Observer.Result(b.target, b.method, ResultSuccess)
	}
	return v, err
}

// Call is the typed form of Execute.
func Call[T any](b *Breaker, fn func() (T, error)) (T, error) {
	v, err := b.Execute(func() (any, error) { return fn() })
	if err != nil {
		var zero T
		return zero, err
	}
	t, _ := v.(T)
	return t, nil
}

// RetryAfter returns how long a client should wait if err is (or wraps) an *OpenError.
func RetryAfter(err error) (time.Duration, bool) {
	var oe *OpenError
	if errors.As(err, &oe) {
		return oe.RetryAfter, true
	}
	return 0, false
}
