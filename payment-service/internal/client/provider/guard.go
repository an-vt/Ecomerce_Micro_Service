package provider

import (
	"context"
	"time"

	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Target is the name of the provider in breaker metrics.
const Target = "payment-provider"

// IsFailure decides what counts against the circuit: only a provider that could not be used. A refusal of the card
// (a business answer) is the provider working as designed.
func IsFailure(err error) bool { return err != nil && !errs.IsPermanent(err) }

// Guard decorates a provider with a deadline per call and a circuit breaker per operation. While the circuit is open
// the provider is not called at all and the error is a *breaker.OpenError (docs/09 §1.2).
type Guard struct {
	inner    domain.Provider
	breakers *breaker.Registry
	timeout  time.Duration
}

// NewGuard wraps inner. The registry should be built with IsFailure as its classifier.
func NewGuard(inner domain.Provider, breakers *breaker.Registry, timeout time.Duration) *Guard {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Guard{inner: inner, breakers: breakers, timeout: timeout}
}

// Charge implements domain.Provider.
func (g *Guard) Charge(ctx context.Context, r domain.ChargeRequest) (string, error) {
	return breaker.Call(g.breakers.Get(Target, "Charge"), func() (string, error) {
		ctx, cancel := context.WithTimeout(ctx, g.timeout)
		defer cancel()
		return g.inner.Charge(ctx, r)
	})
}

// Refund implements domain.Provider.
func (g *Guard) Refund(ctx context.Context, r domain.RefundRequest) error {
	_, err := breaker.Call(g.breakers.Get(Target, "Refund"), func() (struct{}, error) {
		ctx, cancel := context.WithTimeout(ctx, g.timeout)
		defer cancel()
		return struct{}{}, g.inner.Refund(ctx, r)
	})
	return err
}
