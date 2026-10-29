// Package repotest is the repository contract suite: the same behavioural tests run against the in-memory fakes and
// against PostgreSQL, so a fake can never promise something the real repository does not do.
package repotest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Repos are the repositories under test.
type Repos struct {
	Orders domain.OrderRepository
	Sagas  domain.SagaRepository
	Keys   domain.IdempotencyRepository
}

// New returns a fresh, empty set of repositories for one test.
type New func(t *testing.T) Repos

var ctx = context.Background()

// Base is the time the suite's fixtures start at.
var Base = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// Run executes the whole suite.
func Run(t *testing.T, newRepos New) {
	t.Run("Orders", func(t *testing.T) { orders(t, newRepos(t)) })
	t.Run("Listing", func(t *testing.T) { listing(t, newRepos(t)) })
	t.Run("Sagas", func(t *testing.T) { sagas(t, newRepos(t)) })
	t.Run("DueSagas", func(t *testing.T) { dueSagas(t, newRepos(t)) })
	t.Run("IdempotencyKeys", func(t *testing.T) { keys(t, newRepos(t)) })
}

// UUID builds a deterministic id.
func UUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// NewOrder builds a valid order whose creation time is offset by n seconds.
func NewOrder(t *testing.T, n int, user string) *domain.Order {
	t.Helper()
	o, err := domain.NewOrder(domain.NewOrderInput{ID: UUID(n), UserID: user, FeeShip: 5000,
		Items: []domain.Item{
			{ProductID: "p1", ShopID: "s1", Name: "Shirt", Price: 100000, Quantity: 2},
			{ProductID: "p2", ShopID: "s2", Name: "Hat", Price: 50000, Quantity: 1}},
		Discounts: []domain.DiscountRef{{ShopID: "s1", Code: "SALE10"}},
		Shipping:  domain.Shipping{Name: "An", Phone: "0900", Address: "1 Main St", City: "HCMC"}, PaymentMethod: "card", PaymentToken: "tok_ok"},
		Base.Add(time.Duration(n)*time.Second))
	require.NoError(t, err)
	return o
}

func orders(t *testing.T, r Repos) {
	o := NewOrder(t, 1, "alice")
	require.NoError(t, r.Orders.Insert(ctx, o))

	got, err := r.Orders.Find(ctx, o.ID())
	require.NoError(t, err)
	assert.Equal(t, o.Snapshot(), got.Snapshot(), "round trip keeps every field, items and codes in order, and the payment token")

	_, err = r.Orders.Find(ctx, UUID(99))
	assert.Equal(t, domain.CodeOrderNotFound, errs.CodeOf(err))
	_, err = r.Orders.Lock(ctx, UUID(99))
	assert.Equal(t, domain.CodeOrderNotFound, errs.CodeOf(err))

	locked, err := r.Orders.Lock(ctx, o.ID())
	require.NoError(t, err)
	require.NoError(t, locked.Advance(domain.StatusStockReserved, Base.Add(time.Minute)))
	require.NoError(t, locked.ApplyDiscount(25000, Base.Add(time.Minute)))
	require.NoError(t, locked.Advance(domain.StatusDiscountApplied, Base.Add(time.Minute)))
	require.NoError(t, locked.StartCancelling("CARD_DECLINED", Base.Add(2*time.Minute)))
	require.NoError(t, r.Orders.Save(ctx, locked))

	again, err := r.Orders.Find(ctx, o.ID())
	require.NoError(t, err)
	assert.Equal(t, locked.Snapshot(), again.Snapshot())
	assert.Equal(t, domain.StatusCancelling, again.Status())
	assert.EqualValues(t, 25000, again.Snapshot().TotalDiscount)
	assert.Equal(t, "CARD_DECLINED", again.Snapshot().CancelReason)
	assert.EqualValues(t, 3, again.Snapshot().Version)

	assert.Equal(t, domain.CodeOrderNotFound, errs.CodeOf(r.Orders.Save(ctx, NewOrder(t, 50, "alice"))))
}

func listing(t *testing.T, r Repos) {
	for i := 1; i <= 5; i++ {
		require.NoError(t, r.Orders.Insert(ctx, NewOrder(t, 10+i, "alice")))
	}
	require.NoError(t, r.Orders.Insert(ctx, NewOrder(t, 30, "bob")))

	page1, total, err := r.Orders.ListByUser(ctx, "alice", 0, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 5, total, "only the buyer's orders")
	require.Len(t, page1, 2)
	assert.Equal(t, UUID(15), page1[0].ID(), "newest first")
	assert.Equal(t, UUID(14), page1[1].ID())
	assert.Len(t, page1[0].Snapshot().Items, 2, "items come with the order")
	last, _, err := r.Orders.ListByUser(ctx, "alice", 4, 2)
	require.NoError(t, err)
	require.Len(t, last, 1)
	assert.Equal(t, UUID(11), last[0].ID())
	none, total, err := r.Orders.ListByUser(ctx, "nobody", 0, 10)
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.EqualValues(t, 0, total)
}

func newSaga(n int, deadline time.Time) *domain.Saga {
	s, _ := domain.StartSaga(UUID(n), true, Base, domain.Timeouts{Step: time.Second, Compensation: time.Second})
	snap := s.Snapshot()
	snap.StepDeadline = deadline
	return domain.RestoreSaga(snap)
}

func sagas(t *testing.T, r Repos) {
	require.NoError(t, r.Orders.Insert(ctx, NewOrder(t, 40, "alice")))
	s, _ := domain.StartSaga(UUID(40), true, Base, domain.Timeouts{Step: 30 * time.Second, Compensation: 30 * time.Second})
	require.NoError(t, r.Sagas.Insert(ctx, s))

	got, err := r.Sagas.Lock(ctx, UUID(40))
	require.NoError(t, err)
	assert.Equal(t, s.Snapshot(), got.Snapshot(), "round trip keeps every field")

	// compensating state with pending commands and a payment id survives storage
	snap := got.Snapshot()
	snap.State, snap.Pending, snap.Reason, snap.PaymentID, snap.DiscountTotal, snap.Attempts, snap.Version = domain.StateCompensating,
		[]string{"RefundPayment", "ReleaseStock"}, "CARD_DECLINED", "pay-1", 25000, 2, 7
	require.NoError(t, r.Sagas.Save(ctx, domain.RestoreSaga(snap)))
	again, err := r.Sagas.Lock(ctx, UUID(40))
	require.NoError(t, err)
	assert.Equal(t, snap, again.Snapshot())

	_, err = r.Sagas.Lock(ctx, UUID(99))
	assert.Equal(t, domain.CodeSagaNotFound, errs.CodeOf(err))
	assert.Equal(t, domain.CodeSagaNotFound, errs.CodeOf(r.Sagas.Save(ctx, newSaga(98, Base))))
}

func dueSagas(t *testing.T, r Repos) {
	for i := 60; i < 65; i++ {
		require.NoError(t, r.Orders.Insert(ctx, NewOrder(t, i, "alice")))
	}
	now := Base.Add(time.Hour)
	require.NoError(t, r.Sagas.Insert(ctx, newSaga(60, now.Add(-time.Minute)))) // overdue
	require.NoError(t, r.Sagas.Insert(ctx, newSaga(61, now.Add(-time.Hour))))   // more overdue
	require.NoError(t, r.Sagas.Insert(ctx, newSaga(62, now.Add(time.Minute))))  // not due yet
	require.NoError(t, r.Sagas.Insert(ctx, newSaga(63, now)))                   // exactly now: not yet (strictly before)
	done := newSaga(64, now.Add(-time.Hour))                                    // overdue but finished
	snap := done.Snapshot()
	snap.State = domain.StateCompleted
	require.NoError(t, r.Sagas.Insert(ctx, domain.RestoreSaga(snap)))

	first, err := r.Sagas.LockNextDue(ctx, now)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, UUID(61), first.ID(), "the most overdue first")

	// handling moves the deadline, so the next call gives the next one
	moved := first.Snapshot()
	moved.StepDeadline = now.Add(time.Minute)
	require.NoError(t, r.Sagas.Save(ctx, domain.RestoreSaga(moved)))
	second, err := r.Sagas.LockNextDue(ctx, now)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, UUID(60), second.ID())

	moved = second.Snapshot()
	moved.State = domain.StateFailed
	require.NoError(t, r.Sagas.Save(ctx, domain.RestoreSaga(moved)))
	none, err := r.Sagas.LockNextDue(ctx, now)
	require.NoError(t, err)
	assert.Nil(t, none, "finished and not-yet-due sagas are never picked")
}

func keys(t *testing.T, r Repos) {
	rec := domain.IdempotencyRecord{UserID: "alice", Key: "key-1", RequestHash: "abc", OrderID: UUID(1), CreatedAt: Base}
	missing, err := r.Keys.Find(ctx, "alice", "key-1")
	require.NoError(t, err)
	assert.Nil(t, missing)

	claimed, err := r.Keys.Claim(ctx, rec)
	require.NoError(t, err)
	assert.True(t, claimed)
	got, err := r.Keys.Find(ctx, "alice", "key-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, rec, *got)

	other := rec
	other.OrderID, other.RequestHash = UUID(2), "different"
	claimed, err = r.Keys.Claim(ctx, other)
	require.NoError(t, err, "a duplicate is reported, not an error (it must not abort the surrounding transaction)")
	assert.False(t, claimed)
	got, _ = r.Keys.Find(ctx, "alice", "key-1")
	assert.Equal(t, UUID(1), got.OrderID, "the first claim stays")

	bob := rec
	bob.UserID = "bob"
	claimed, err = r.Keys.Claim(ctx, bob)
	require.NoError(t, err)
	assert.True(t, claimed, "keys are per buyer")
}
