package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func newPayment(t *testing.T) *domain.Payment {
	t.Helper()
	p, err := domain.New(domain.NewInput{ID: "p1", OrderID: "o1", UserID: "u1", Amount: 5000, Currency: "vnd", Method: " card "}, now)
	require.NoError(t, err)
	return p
}

func TestNewNormalizesAndStartsPending(t *testing.T) {
	p := newPayment(t)
	s := p.Snapshot()
	assert.Equal(t, domain.StatusPending, s.Status)
	assert.Equal(t, "VND", s.Currency)
	assert.Equal(t, "card", s.Method)
	assert.Equal(t, now, s.CreatedAt)
	assert.Equal(t, "o1", p.OrderID())
	assert.Equal(t, "p1", p.ID())
	assert.EqualValues(t, 5000, p.Amount())
	assert.Equal(t, s, domain.Restore(s).Snapshot())

	free, err := domain.New(domain.NewInput{ID: "p", OrderID: "o", UserID: "u", Amount: 0, Currency: "VND"}, now)
	require.NoError(t, err, "a fully discounted order costs nothing and needs no method")
	assert.EqualValues(t, 0, free.Amount())
}

func TestNewRefusals(t *testing.T) {
	ok := domain.NewInput{ID: "p", OrderID: "o", UserID: "u", Amount: 1, Currency: "VND", Method: "card"}
	for name, mut := range map[string]func(*domain.NewInput){
		"no id":        func(in *domain.NewInput) { in.ID = "" },
		"no order":     func(in *domain.NewInput) { in.OrderID = "" },
		"no user":      func(in *domain.NewInput) { in.UserID = "" },
		"negative":     func(in *domain.NewInput) { in.Amount = -1 },
		"bad currency": func(in *domain.NewInput) { in.Currency = "DONG" },
		"no method":    func(in *domain.NewInput) { in.Method = " " },
	} {
		in := ok
		mut(&in)
		_, err := domain.New(in, now)
		require.Error(t, err, name)
		assert.Equal(t, domain.CodeInvalidPayment, errs.CodeOf(err), name)
	}
}

func TestLifecycle(t *testing.T) {
	p := newPayment(t)
	require.NoError(t, p.Succeed("ref-1", now.Add(time.Second)))
	assert.Equal(t, domain.StatusSucceeded, p.Status())
	assert.Equal(t, "ref-1", p.ProviderRef())
	assert.True(t, p.NeedsRefund())
	require.NoError(t, p.Refund(now.Add(2*time.Second)))
	assert.Equal(t, domain.StatusRefunded, p.Status())
	assert.False(t, p.NeedsRefund())
	assert.Equal(t, now.Add(2*time.Second), p.Snapshot().UpdatedAt)
}

func TestFailureAndCancellationKeepAReason(t *testing.T) {
	p := newPayment(t)
	require.NoError(t, p.Fail("CARD_DECLINED", now))
	assert.Equal(t, domain.StatusFailed, p.Status())
	assert.Equal(t, "CARD_DECLINED", p.FailureReason())
	assert.False(t, p.NeedsRefund())

	c := newPayment(t)
	require.NoError(t, c.Cancel(now))
	assert.Equal(t, domain.StatusCancelled, c.Status())
}

func TestTransitionsOnlyFromTheRightState(t *testing.T) {
	succeeded := newPayment(t)
	require.NoError(t, succeeded.Succeed("r", now))
	failed := newPayment(t)
	require.NoError(t, failed.Fail("X", now))

	for name, err := range map[string]error{
		"succeed twice":         succeeded.Succeed("r2", now),
		"fail after success":    succeeded.Fail("X", now),
		"cancel after success":  succeeded.Cancel(now),
		"succeed after failure": failed.Succeed("r", now),
		"fail twice":            failed.Fail("Y", now),
		"refund a failure":      failed.Refund(now),
		"refund a pending one":  newPayment(t).Refund(now),
		"cancel a failure":      failed.Cancel(now),
	} {
		require.Error(t, err, name)
		assert.Equal(t, domain.CodeInvalidState, errs.CodeOf(err), name)
	}
	assert.Equal(t, "ref", func() string {
		p := newPayment(t)
		_ = p.Succeed("ref", now)
		_ = p.Succeed("other", now)
		return p.ProviderRef()
	}(), "a refused transition changes nothing")
}

func TestCancelledTombstone(t *testing.T) {
	c, err := domain.NewCancelled("p", "o", "vnd", now)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusCancelled, c.Status())
	assert.Equal(t, "VND", c.Snapshot().Currency)
	assert.False(t, c.NeedsRefund())
	for _, args := range [][3]string{{"", "o", "VND"}, {"p", "", "VND"}, {"p", "o", "V"}} {
		_, err := domain.NewCancelled(args[0], args[1], args[2], now)
		assert.Error(t, err)
	}
}

func TestErrors(t *testing.T) {
	assert.Equal(t, errs.KindNotFound, errs.KindOf(domain.ErrNotFound()))
	assert.Equal(t, errs.KindFailedPrecondition, errs.KindOf(domain.ErrCardDeclined()))
	assert.True(t, errs.IsPermanent(domain.ErrCardDeclined()), "a decline is final, not a reason to retry")
}
