// Package clock abstracts time so that domain and use-case code is testable without sleeping.
package clock

import (
	"sync"
	"time"
)

// Clock returns the current time.
type Clock interface {
	Now() time.Time
}

// System is the real clock (UTC).
type System struct{}

// Now implements Clock.
func (System) Now() time.Time { return time.Now().UTC() }

// Fixed is a settable clock for tests. Safe for concurrent use.
type Fixed struct {
	mu  sync.Mutex
	now time.Time
}

// NewFixed returns a Fixed clock at t.
func NewFixed(t time.Time) *Fixed { return &Fixed{now: t} }

// Now implements Clock.
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set moves the clock to t.
func (f *Fixed) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Advance moves the clock forward by d.
func (f *Fixed) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
