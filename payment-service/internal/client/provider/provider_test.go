package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/payment-service/internal/client/provider"
	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/errs"
)

func charge(token string) domain.ChargeRequest {
	return domain.ChargeRequest{Key: "order-1", Amount: 1000, Currency: "VND", Method: "card", Token: token}
}

func TestMockTokens(t *testing.T) {
	m := provider.NewMock()
	ref, err := m.Charge(context.Background(), charge(provider.TokenOK))
	require.NoError(t, err)
	assert.Equal(t, "mock_ch_order-1", ref)

	_, err = m.Charge(context.Background(), domain.ChargeRequest{Key: "o2", Token: provider.TokenFail})
	assert.Equal(t, domain.CodeCardDeclined, errs.CodeOf(err))
	_, err = m.Charge(context.Background(), domain.ChargeRequest{Key: "o3", Token: "tok_who_knows"})
	assert.Equal(t, domain.CodeCardDeclined, errs.CodeOf(err), "unknown tokens are declined")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = m.Charge(ctx, domain.ChargeRequest{Key: "o4", Token: provider.TokenTimeout})
	assert.ErrorIs(t, err, context.DeadlineExceeded, "tok_timeout hangs until the caller gives up")
}

func TestMockIsIdempotentPerKey(t *testing.T) {
	m := provider.NewMock()
	a, _ := m.Charge(context.Background(), charge(provider.TokenOK))
	b, err := m.Charge(context.Background(), charge(provider.TokenFail)) // even a different token: same key, same charge
	require.NoError(t, err)
	assert.Equal(t, a, b)

	require.NoError(t, m.Refund(context.Background(), domain.RefundRequest{Key: "order-1", ProviderRef: a}))
	require.NoError(t, m.Refund(context.Background(), domain.RefundRequest{Key: "order-1", ProviderRef: a}))
	assert.True(t, m.Refunded("order-1"))
	assert.False(t, m.Refunded("other"))
	assert.Error(t, m.Refund(context.Background(), domain.RefundRequest{Key: "x"}))
	assert.Equal(t, 5, m.Calls())
}

func guard(m domain.Provider, timeout time.Duration, min uint32) (*provider.Guard, *breaker.Registry) {
	reg := breaker.NewRegistry(breaker.Settings{ErrorRatio: 0.5, MinRequests: min, ResetTimeout: time.Minute, IsFailure: provider.IsFailure})
	return provider.NewGuard(m, reg, timeout), reg
}

// Acceptance #3: tok_timeout x N opens the circuit, after which the provider is not called at all.
func TestTimeoutsOpenTheCircuitAndStopCallingTheProvider(t *testing.T) {
	m := provider.NewMock()
	g, reg := guard(m, 20*time.Millisecond, 3)

	for i := 0; i < 3; i++ {
		_, err := g.Charge(context.Background(), domain.ChargeRequest{Key: "k", Token: provider.TokenTimeout})
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
	assert.Equal(t, breaker.Open, reg.Get(provider.Target, "Charge").State())
	before := m.Calls()

	start := time.Now()
	_, err := g.Charge(context.Background(), domain.ChargeRequest{Key: "k2", Token: provider.TokenOK})
	assert.Less(t, time.Since(start), 10*time.Millisecond, "fails fast")
	_, isOpen := breaker.RetryAfter(err)
	assert.True(t, isOpen)
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err))
	assert.Equal(t, before, m.Calls(), "an open circuit does not reach the provider")
	assert.Equal(t, breaker.Closed, reg.Get(provider.Target, "Refund").State(), "charge and refund have separate circuits")
}

// A declined card is the provider doing its job: it must never open the circuit.
func TestDeclinesDoNotOpenTheCircuit(t *testing.T) {
	g, reg := guard(provider.NewMock(), time.Second, 3)
	for i := 0; i < 10; i++ {
		_, err := g.Charge(context.Background(), domain.ChargeRequest{Key: "k", Token: provider.TokenFail})
		require.Equal(t, domain.CodeCardDeclined, errs.CodeOf(err))
	}
	assert.Equal(t, breaker.Closed, reg.Get(provider.Target, "Charge").State())
	ref, err := g.Charge(context.Background(), domain.ChargeRequest{Key: "ok", Token: provider.TokenOK})
	require.NoError(t, err)
	assert.NotEmpty(t, ref)
}

func TestGuardRefundAndDefaultTimeout(t *testing.T) {
	m := provider.NewMock()
	g, _ := guard(m, 0, 3) // 0 -> the 3 s default
	require.NoError(t, g.Refund(context.Background(), domain.RefundRequest{Key: "o", ProviderRef: "ref"}))
	assert.True(t, m.Refunded("o"))
	assert.Error(t, g.Refund(context.Background(), domain.RefundRequest{Key: "o"}))
	assert.False(t, provider.IsFailure(nil))
	assert.False(t, provider.IsFailure(domain.ErrCardDeclined()))
	assert.True(t, provider.IsFailure(context.DeadlineExceeded))
}
