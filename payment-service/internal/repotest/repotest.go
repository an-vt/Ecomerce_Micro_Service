// Package repotest is the repository contract suite: the same behavioural tests run against the in-memory fake and
// against PostgreSQL, so a fake can never promise something the real repository does not do.
package repotest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// New returns a fresh, empty repository for one test.
type New func(t *testing.T) domain.PaymentRepository

var ctx = context.Background()
var base = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// Run executes the whole suite.
func Run(t *testing.T, newRepo New) {
	t.Run("RoundTrip", func(t *testing.T) { roundTrip(t, newRepo(t)) })
	t.Run("OnePaymentPerOrder", func(t *testing.T) { onePerOrder(t, newRepo(t)) })
	t.Run("Tombstone", func(t *testing.T) { tombstone(t, newRepo(t)) })
}

// UUID builds a deterministic id.
func UUID(n int) string { return "00000000-0000-4000-8000-" + pad(n) }

func pad(n int) string {
	s := "000000000000"
	d := []byte(s)
	for i := len(d) - 1; n > 0 && i >= 0; i-- {
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d)
}

func payment(t *testing.T, n int, order string) *domain.Payment {
	t.Helper()
	p, err := domain.New(domain.NewInput{ID: UUID(n), OrderID: order, UserID: "u1", Amount: 5000, Currency: "VND", Method: "card"}, base)
	require.NoError(t, err)
	return p
}

func roundTrip(t *testing.T, r domain.PaymentRepository) {
	p := payment(t, 1, "order-1")
	inserted, err := r.Insert(ctx, p)
	require.NoError(t, err)
	assert.True(t, inserted)

	got, err := r.LockByOrder(ctx, "order-1")
	require.NoError(t, err)
	assert.Equal(t, p.Snapshot(), got.Snapshot(), "round trip keeps every field")

	require.NoError(t, got.Succeed("prov-ref-1", base.Add(time.Second)))
	require.NoError(t, r.Save(ctx, got))
	again, err := r.LockByOrder(ctx, "order-1")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusSucceeded, again.Status())
	assert.Equal(t, "prov-ref-1", again.ProviderRef())
	assert.Equal(t, base.Add(time.Second), again.Snapshot().UpdatedAt)

	require.NoError(t, again.Refund(base.Add(2*time.Second)))
	require.NoError(t, r.Save(ctx, again))
	last, _ := r.LockByOrder(ctx, "order-1")
	assert.Equal(t, domain.StatusRefunded, last.Status())

	f := payment(t, 2, "order-2")
	_, _ = r.Insert(ctx, f)
	fl, _ := r.LockByOrder(ctx, "order-2")
	require.NoError(t, fl.Fail("CARD_DECLINED", base))
	require.NoError(t, r.Save(ctx, fl))
	fl, _ = r.LockByOrder(ctx, "order-2")
	assert.Equal(t, "CARD_DECLINED", fl.FailureReason())

	_, err = r.LockByOrder(ctx, "ghost")
	assert.Equal(t, domain.CodePaymentNotFound, errs.CodeOf(err))
	assert.Equal(t, domain.CodePaymentNotFound, errs.CodeOf(r.Save(ctx, payment(t, 9, "ghost"))))
}

// The unique order is what prevents a double charge when two copies of a command race.
func onePerOrder(t *testing.T, r domain.PaymentRepository) {
	first := payment(t, 10, "order-x")
	inserted, err := r.Insert(ctx, first)
	require.NoError(t, err)
	require.True(t, inserted)

	second := payment(t, 11, "order-x")
	inserted, err = r.Insert(ctx, second)
	require.NoError(t, err, "a duplicate is reported, not an error (it must not abort the surrounding transaction)")
	assert.False(t, inserted)

	got, err := r.LockByOrder(ctx, "order-x")
	require.NoError(t, err)
	assert.Equal(t, UUID(10), got.ID(), "the first one stays")
}

func tombstone(t *testing.T, r domain.PaymentRepository) {
	c, err := domain.NewCancelled(UUID(20), "order-c", "VND", base)
	require.NoError(t, err)
	inserted, err := r.Insert(ctx, c)
	require.NoError(t, err)
	assert.True(t, inserted)

	got, err := r.LockByOrder(ctx, "order-c")
	require.NoError(t, err)
	assert.Equal(t, c.Snapshot(), got.Snapshot(), "a tombstone has no user, method or amount and still round-trips")
	assert.Equal(t, domain.StatusCancelled, got.Status())

	late := payment(t, 21, "order-c")
	inserted, err = r.Insert(ctx, late)
	require.NoError(t, err)
	assert.False(t, inserted, "a late charge cannot replace the tombstone")
}
