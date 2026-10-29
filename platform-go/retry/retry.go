// Package retry provides exponential backoff with full jitter (docs/09-resilience.md §2).
package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

// Policy configures Do. Zero values get safe defaults.
type Policy struct {
	MaxAttempts int           // total tries including the first; default 3
	Base        time.Duration // first delay ceiling; default 100ms
	Max         time.Duration // delay ceiling; default 5s
	// Retryable decides whether err is worth another try; nil means every error is.
	Retryable func(error) bool
	// Sleep waits for d or until ctx is done; tests replace it. Default: real timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Jitter returns a value in [0,1); tests replace it. Default: math/rand/v2.
	Jitter func() float64
}

// Backoff returns the delay before retry number attempt (1-based): a random value in [0, min(max, base*2^(attempt-1))).
func Backoff(attempt int, base, max time.Duration, jitter func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if jitter == nil {
		jitter = rand.Float64
	}
	ceil := base
	for i := 1; i < attempt && ceil < max; i++ {
		ceil *= 2
	}
	if ceil > max {
		ceil = max
	}
	return time.Duration(float64(ceil) * jitter())
}

// Do calls fn until it succeeds, returns a non-retryable error, the attempts are used up, or ctx is done.
// It returns the last error from fn (or ctx's error if the wait was interrupted).
func Do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	if p.Base <= 0 {
		p.Base = 100 * time.Millisecond
	}
	if p.Max <= 0 {
		p.Max = 5 * time.Second
	}
	if p.Sleep == nil {
		p.Sleep = sleep
	}
	var err error
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if p.Retryable != nil && !p.Retryable(err) {
			return err
		}
		if attempt == p.MaxAttempts {
			break
		}
		if serr := p.Sleep(ctx, Backoff(attempt, p.Base, p.Max, p.Jitter)); serr != nil {
			return serr
		}
	}
	return err
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
