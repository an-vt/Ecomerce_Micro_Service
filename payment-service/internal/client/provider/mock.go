// Package provider holds the payment provider adapters: a mock that behaves like a card processor for tests and local
// runs, and the guard that puts a deadline and a circuit breaker in front of any provider.
package provider

import (
	"context"
	"sync"

	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Tokens understood by the mock (docs/01 F-22).
const (
	TokenOK      = "tok_ok"      // the charge succeeds
	TokenFail    = "tok_fail"    // the card is declined
	TokenTimeout = "tok_timeout" // the provider hangs until the caller gives up
)

// Mock is a payment provider that is deterministic and idempotent, like a real one given an idempotency key.
type Mock struct {
	mu       sync.Mutex
	charges  map[string]string // key -> provider ref
	refunded map[string]bool   // key -> done
	calls    int
}

// NewMock creates an empty mock.
func NewMock() *Mock { return &Mock{charges: map[string]string{}, refunded: map[string]bool{}} }

// Charge implements domain.Provider.
func (m *Mock) Charge(ctx context.Context, r domain.ChargeRequest) (string, error) {
	m.mu.Lock()
	m.calls++
	if ref, ok := m.charges[r.Key]; ok { // the same key is the same charge
		m.mu.Unlock()
		return ref, nil
	}
	m.mu.Unlock()

	switch r.Token {
	case TokenOK:
		m.mu.Lock()
		defer m.mu.Unlock()
		ref := "mock_ch_" + r.Key
		m.charges[r.Key] = ref
		return ref, nil
	case TokenTimeout:
		<-ctx.Done()
		return "", ctx.Err()
	default: // tok_fail and anything the mock does not know
		return "", domain.ErrCardDeclined()
	}
}

// Refund implements domain.Provider.
func (m *Mock) Refund(_ context.Context, r domain.RefundRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if r.ProviderRef == "" {
		return errs.InvalidArgument("NO_CHARGE", "nothing to refund")
	}
	m.refunded[r.Key] = true
	return nil
}

// Calls is how many times the mock was called (charges and refunds); tests use it to prove "no call was made".
func (m *Mock) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// Refunded reports whether a refund with this key was made.
func (m *Mock) Refunded(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refunded[key]
}
