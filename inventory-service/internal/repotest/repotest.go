// Package repotest is the repository contract suite: the same behavioural tests run against the in-memory fakes and
// against PostgreSQL, so a fake can never promise something the real repository does not do.
package repotest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Repos are the repositories under test.
type Repos struct {
	Inventories  domain.InventoryRepository
	Reservations domain.ReservationRepository
}

// New returns a fresh, empty set of repositories for one test.
type New func(t *testing.T) Repos

var ctx = context.Background()
var base = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// Run executes the whole suite.
func Run(t *testing.T, newRepos New) {
	t.Run("Inventories", func(t *testing.T) { inventories(t, newRepos(t)) })
	t.Run("Reservations", func(t *testing.T) { reservations(t, newRepos(t)) })
}

func inv(t *testing.T, id, product string, stock int64) *domain.Inventory {
	t.Helper()
	i, err := domain.NewInventory(id, product, "11111111-1111-4111-8111-111111111111", stock, base)
	require.NoError(t, err)
	return i
}

func inventories(t *testing.T, r Repos) {
	a := inv(t, "00000000-0000-4000-8000-0000000000a1", "prod-a", 10)
	created, err := r.Inventories.Insert(ctx, a)
	require.NoError(t, err)
	assert.True(t, created)

	again := inv(t, "00000000-0000-4000-8000-0000000000a2", "prod-a", 999)
	created, err = r.Inventories.Insert(ctx, again)
	require.NoError(t, err)
	assert.False(t, created, "a second record for the same product is ignored, not an error")

	got, err := r.Inventories.Find(ctx, "prod-a")
	require.NoError(t, err)
	assert.Equal(t, a.Snapshot(), got.Snapshot(), "the first record won; round trip keeps every field")

	_, err = r.Inventories.Find(ctx, "ghost")
	assert.Equal(t, domain.CodeInventoryNotFound, errs.CodeOf(err))

	_, _ = r.Inventories.Insert(ctx, inv(t, "00000000-0000-4000-8000-0000000000b1", "prod-b", 5))
	locked, err := r.Inventories.Lock(ctx, []string{"prod-b", "ghost", "prod-a"})
	require.NoError(t, err)
	assert.Len(t, locked, 2, "unknown ids are absent")
	assert.Equal(t, int64(5), locked["prod-b"].Stock())

	require.NoError(t, locked["prod-a"].Reserve(4, base.Add(time.Minute)))
	require.NoError(t, r.Inventories.Save(ctx, locked["prod-a"]))
	got, _ = r.Inventories.Find(ctx, "prod-a")
	assert.Equal(t, [2]int64{6, 4}, [2]int64{got.Stock(), got.Reserved()})

	require.NoError(t, got.AddStock(1, "Hall 3", base.Add(2*time.Minute)))
	require.NoError(t, r.Inventories.Save(ctx, got))
	got, _ = r.Inventories.Find(ctx, "prod-a")
	assert.Equal(t, "Hall 3", got.Location())

	ghost := inv(t, "00000000-0000-4000-8000-0000000000c1", "ghost", 1)
	assert.Equal(t, domain.CodeInventoryNotFound, errs.CodeOf(r.Inventories.Save(ctx, ghost)))
	none, err := r.Inventories.Lock(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

func res(t *testing.T, id, order, product string, q int64, exp time.Time) *domain.Reservation {
	t.Helper()
	r, err := domain.NewReservation(id, order, product, q, exp, base)
	require.NoError(t, err)
	return r
}

func reservations(t *testing.T, r Repos) {
	o1 := "aaaaaaaa-0000-4000-8000-000000000001"
	o2 := "aaaaaaaa-0000-4000-8000-000000000002"
	r1 := res(t, "00000000-0000-4000-8000-0000000000d1", o1, "prod-a", 2, base.Add(15*time.Minute))
	r2 := res(t, "00000000-0000-4000-8000-0000000000d2", o1, "prod-b", 1, base.Add(15*time.Minute))
	require.NoError(t, r.Reservations.Insert(ctx, []*domain.Reservation{r1, r2}))

	got, err := r.Reservations.ForOrder(ctx, o1)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, r1.Snapshot(), got[0].Snapshot())
	assert.Equal(t, "prod-b", got[1].ProductID(), "ordered by product")
	none, err := r.Reservations.ForOrder(ctx, o2)
	require.NoError(t, err)
	assert.Empty(t, none)

	dup := res(t, "00000000-0000-4000-8000-0000000000d3", o1, "prod-a", 9, base.Add(time.Hour))
	err = r.Reservations.Insert(ctx, []*domain.Reservation{dup})
	assert.Equal(t, domain.CodeAlreadyReserved, errs.CodeOf(err), "an order holds each product once")
	got, _ = r.Reservations.ForOrder(ctx, o1)
	assert.Len(t, got, 2, "the rejected insert left nothing behind")

	require.NoError(t, got[0].Commit(base.Add(time.Minute)))
	require.NoError(t, r.Reservations.Save(ctx, got[0]))
	again, _ := r.Reservations.ForOrder(ctx, o1)
	assert.Equal(t, domain.ReservationCommitted, again[0].Status())
	assert.Equal(t, domain.ReservationReserved, again[1].Status())

	// expiry: o2 is overdue, o1 still has one overdue active hold (prod-b), a fresh order is not due
	require.NoError(t, r.Reservations.Insert(ctx, []*domain.Reservation{res(t, "00000000-0000-4000-8000-0000000000d4", o2, "prod-a", 1, base.Add(time.Minute))}))
	o3 := "aaaaaaaa-0000-4000-8000-000000000003"
	require.NoError(t, r.Reservations.Insert(ctx, []*domain.Reservation{res(t, "00000000-0000-4000-8000-0000000000d5", o3, "prod-a", 1, base.Add(time.Hour))}))
	due, err := r.Reservations.ExpiredOrders(ctx, base.Add(20*time.Minute), 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{o1, o2}, due, "only orders with an active hold past its deadline")
	due, _ = r.Reservations.ExpiredOrders(ctx, base.Add(20*time.Minute), 1)
	assert.Len(t, due, 1, "limit is honoured")
	due, _ = r.Reservations.ExpiredOrders(ctx, base, 10)
	assert.Empty(t, due)

	ghost := res(t, "00000000-0000-4000-8000-0000000000ff", o1, "prod-z", 1, base)
	assert.Error(t, r.Reservations.Save(ctx, ghost))
}
