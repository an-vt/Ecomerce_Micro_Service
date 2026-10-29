package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/payment-service/internal/client/provider"
	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/payment-service/internal/fake"
	"github.com/an-vt/ecom/payment-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/saga"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

type rig struct {
	uc   *usecase.Set
	w    *fake.World
	prov *fake.Provider
}

func newRig() *rig {
	w := fake.NewWorld()
	p := &fake.Provider{}
	return &rig{uc: usecase.NewSet(&usecase.Deps{Payments: w.Payments(), Provider: p, Tx: w, Outbox: w, Clock: clock.NewFixed(now), IDs: idgen.UUID{}, Currency: "VND"}), w: w, prov: p}
}

func charge(order, token string) saga.ProcessPayment {
	return saga.ProcessPayment{OrderID: order, UserID: "alice", Amount: 150000, Method: "card", Token: token}
}

func (r *rig) last(t *testing.T, v any) string {
	t.Helper()
	types := r.w.Types()
	require.NotEmpty(t, types)
	require.NoError(t, r.w.Decode(len(types)-1, v))
	return types[len(types)-1]
}

// Acceptance #1
func TestAChargeThatSucceedsRepliesPaymentCompleted(t *testing.T) {
	r := newRig()
	require.NoError(t, r.uc.ProcessPayment.Execute(context.Background(), charge("o1", provider.TokenOK)))
	var got saga.PaymentCompleted
	assert.Equal(t, saga.ReplyPaymentCompleted, r.last(t, &got))
	assert.Equal(t, "o1", got.OrderID)
	assert.NotEmpty(t, got.PaymentID)

	s, ok := r.w.Payment("o1")
	require.True(t, ok)
	assert.Equal(t, domain.StatusSucceeded, s.Status)
	assert.Equal(t, "ref_o1", s.ProviderRef)
	assert.Equal(t, "VND", s.Currency)
	require.Len(t, r.prov.ChargeCalls, 1)
	assert.Equal(t, domain.ChargeRequest{Key: "o1", Amount: 150000, Currency: "VND", Method: "card", Token: provider.TokenOK}, r.prov.ChargeCalls[0],
		"the order id is the provider's idempotency key")
}

// Acceptance #2
func TestADeclinedCardRepliesPaymentFailed(t *testing.T) {
	r := newRig()
	r.prov.ChargeFn = func(context.Context, domain.ChargeRequest) (string, error) { return "", domain.ErrCardDeclined() }
	require.NoError(t, r.uc.ProcessPayment.Execute(context.Background(), charge("o1", provider.TokenFail)), "a decline is an answer, not a handler error")
	var got saga.PaymentFailed
	assert.Equal(t, saga.ReplyPaymentFailed, r.last(t, &got))
	assert.Equal(t, saga.ReasonCardDeclined, got.Reason)
	s, _ := r.w.Payment("o1")
	assert.Equal(t, domain.StatusFailed, s.Status)
	assert.Equal(t, saga.ReasonCardDeclined, s.FailureReason)
}

func TestAProviderThatCannotBeUsedRepliesProviderUnavailable(t *testing.T) {
	for name, cause := range map[string]error{
		"timeout":      context.DeadlineExceeded,
		"open circuit": errs.Unavailable(breaker.CodeOpen, "upstream payment-provider is unavailable"),
		"other":        errors.New("connection reset"),
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			r.prov.ChargeFn = func(context.Context, domain.ChargeRequest) (string, error) { return "", cause }
			require.NoError(t, r.uc.ProcessPayment.Execute(context.Background(), charge("o1", provider.TokenTimeout)))
			var got saga.PaymentFailed
			assert.Equal(t, saga.ReplyPaymentFailed, r.last(t, &got))
			assert.Equal(t, saga.ReasonProviderUnavailable, got.Reason)
		})
	}
}

func TestShuttingDownLeavesTheMessageToBeRetried(t *testing.T) {
	r := newRig()
	ctx, cancel := context.WithCancel(context.Background())
	r.prov.ChargeFn = func(context.Context, domain.ChargeRequest) (string, error) { cancel(); return "", context.Canceled }
	require.Error(t, r.uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))
	_, ok := r.w.Payment("o1")
	assert.False(t, ok, "nothing was recorded: the retry starts clean")
	assert.Empty(t, r.w.Types())
}

// Acceptance #5
func TestAChargeIsMadeOnceHoweverOftenTheCommandArrives(t *testing.T) {
	r := newRig()
	for i := 0; i < 4; i++ {
		require.NoError(t, r.uc.ProcessPayment.Execute(context.Background(), charge("o1", provider.TokenOK)))
	}
	assert.Len(t, r.prov.ChargeCalls, 1, "one charge")
	assert.Equal(t, []string{saga.ReplyPaymentCompleted, saga.ReplyPaymentCompleted, saga.ReplyPaymentCompleted, saga.ReplyPaymentCompleted}, r.w.Types())
	var first, last saga.PaymentCompleted
	require.NoError(t, r.w.Decode(0, &first))
	require.NoError(t, r.w.Decode(3, &last))
	assert.Equal(t, first.PaymentID, last.PaymentID, "every copy gets the same answer")
}

func TestARepeatedDeclineIsNotRetriedAtTheProvider(t *testing.T) {
	r := newRig()
	r.prov.ChargeFn = func(context.Context, domain.ChargeRequest) (string, error) { return "", domain.ErrCardDeclined() }
	for i := 0; i < 3; i++ {
		require.NoError(t, r.uc.ProcessPayment.Execute(context.Background(), charge("o1", provider.TokenFail)))
	}
	assert.Len(t, r.prov.ChargeCalls, 1)
	var got saga.PaymentFailed
	r.last(t, &got)
	assert.Equal(t, saga.ReasonCardDeclined, got.Reason, "the stored reason is replayed")
}

func TestAFullyDiscountedOrderCostsNothingAndSkipsTheProvider(t *testing.T) {
	r := newRig()
	cmd := charge("o1", "")
	cmd.Amount, cmd.Method = 0, ""
	require.NoError(t, r.uc.ProcessPayment.Execute(context.Background(), cmd))
	var got saga.PaymentCompleted
	assert.Equal(t, saga.ReplyPaymentCompleted, r.last(t, &got))
	assert.Empty(t, r.prov.ChargeCalls)

	// and refunding it needs no provider call either
	require.NoError(t, r.uc.RefundPayment.Execute(context.Background(), saga.RefundPayment{OrderID: "o1"}))
	assert.Empty(t, r.prov.RefundCalls)
	s, _ := r.w.Payment("o1")
	assert.Equal(t, domain.StatusRefunded, s.Status)
}

func TestACommandThatCannotBeAPaymentFailsTheStep(t *testing.T) {
	for name, mut := range map[string]func(*saga.ProcessPayment){
		"no user":         func(c *saga.ProcessPayment) { c.UserID = "" },
		"negative amount": func(c *saga.ProcessPayment) { c.Amount = -5 },
		"no method":       func(c *saga.ProcessPayment) { c.Method = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig()
			cmd := charge("o1", provider.TokenOK)
			mut(&cmd)
			require.NoError(t, r.uc.ProcessPayment.Execute(context.Background(), cmd), "a reply, so the saga compensates instead of waiting")
			var got saga.PaymentFailed
			assert.Equal(t, saga.ReplyPaymentFailed, r.last(t, &got))
			assert.Equal(t, usecase.ReasonInvalidRequest, got.Reason)
			_, ok := r.w.Payment("o1")
			assert.False(t, ok)
			assert.Empty(t, r.prov.ChargeCalls)
		})
	}
}

func TestTheOutcomeAndTheReplyCommitTogether(t *testing.T) {
	r := newRig()
	r.w.FailOutbox = errors.New("outbox down")
	require.Error(t, r.uc.ProcessPayment.Execute(context.Background(), charge("o1", provider.TokenOK)))
	_, ok := r.w.Payment("o1")
	assert.False(t, ok, "the payment rolled back with the reply")
	r.w.FailOutbox = nil
	require.NoError(t, r.uc.ProcessPayment.Execute(context.Background(), charge("o1", provider.TokenOK)))
	var got saga.PaymentCompleted
	assert.Equal(t, saga.ReplyPaymentCompleted, r.last(t, &got))
	assert.Len(t, r.prov.ChargeCalls, 2, "the retry asks the provider again with the same key, which a real provider answers with the same charge")
	assert.Equal(t, r.prov.ChargeCalls[0].Key, r.prov.ChargeCalls[1].Key)
}

func TestRefundGivesTheMoneyBackOnce(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	require.NoError(t, r.uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))
	for i := 0; i < 3; i++ {
		require.NoError(t, r.uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"}))
		var got saga.PaymentRefunded
		assert.Equal(t, saga.ReplyPaymentRefunded, r.last(t, &got))
		assert.Equal(t, "o1", got.OrderID)
	}
	require.Len(t, r.prov.RefundCalls, 1, "refunded once, not three times")
	assert.Equal(t, domain.RefundRequest{Key: "o1", ProviderRef: "ref_o1", Amount: 150000, Currency: "VND"}, r.prov.RefundCalls[0])
	s, _ := r.w.Payment("o1")
	assert.Equal(t, domain.StatusRefunded, s.Status)
}

// Acceptance #4
func TestARefundBeforeTheChargeLeavesATombstoneThatRefusesTheLateCharge(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	require.NoError(t, r.uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"}))
	var refunded saga.PaymentRefunded
	assert.Equal(t, saga.ReplyPaymentRefunded, r.last(t, &refunded))
	s, ok := r.w.Payment("o1")
	require.True(t, ok)
	assert.Equal(t, domain.StatusCancelled, s.Status)
	assert.Empty(t, r.prov.RefundCalls, "nothing was charged, nothing to refund")

	require.NoError(t, r.uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))
	var failed saga.PaymentFailed
	assert.Equal(t, saga.ReplyPaymentFailed, r.last(t, &failed))
	assert.Equal(t, saga.ReasonStepTimeout, failed.Reason)
	assert.Empty(t, r.prov.ChargeCalls, "the late command never reaches the provider")
}

func TestARefundAfterAFailureOrARefundHasNothingToUndo(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	r.prov.ChargeFn = func(context.Context, domain.ChargeRequest) (string, error) { return "", domain.ErrCardDeclined() }
	require.NoError(t, r.uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenFail)))
	require.NoError(t, r.uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"}))
	var got saga.PaymentRefunded
	assert.Equal(t, saga.ReplyPaymentRefunded, r.last(t, &got), "compensations always answer")
	s, _ := r.w.Payment("o1")
	assert.Equal(t, domain.StatusFailed, s.Status, "a failed payment stays failed")
	assert.Empty(t, r.prov.RefundCalls)
}

func TestAProcessAfterARefundIsRefused(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	require.NoError(t, r.uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))
	require.NoError(t, r.uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"}))
	require.NoError(t, r.uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))
	var failed saga.PaymentFailed
	assert.Equal(t, saga.ReplyPaymentFailed, r.last(t, &failed))
	assert.Len(t, r.prov.ChargeCalls, 1)
}

func TestARefundTheProviderCannotDoIsRetriedNotDropped(t *testing.T) {
	r := newRig()
	ctx := context.Background()
	require.NoError(t, r.uc.ProcessPayment.Execute(ctx, charge("o1", provider.TokenOK)))
	r.prov.RefundFn = func(context.Context, domain.RefundRequest) error { return errs.Unavailable("X", "provider down") }
	before := len(r.w.Types())

	err := r.uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"})
	require.Error(t, err, "the handler fails so the message is retried; the saga keeps compensating")
	assert.Equal(t, before, len(r.w.Types()), "no PaymentRefunded was announced")
	s, _ := r.w.Payment("o1")
	assert.Equal(t, domain.StatusSucceeded, s.Status, "the charge still stands until it is really refunded")

	r.prov.RefundFn = nil
	require.NoError(t, r.uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: "o1"}))
	s, _ = r.w.Payment("o1")
	assert.Equal(t, domain.StatusRefunded, s.Status)
}
