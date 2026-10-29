package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func inv(t *testing.T, stock int64) *domain.Inventory {
	t.Helper()
	i, err := domain.NewInventory("i1", "p1", "shop1", stock, now)
	require.NoError(t, err)
	return i
}

func TestNewInventory(t *testing.T) {
	i := inv(t, 10)
	assert.Equal(t, int64(10), i.Stock())
	assert.Equal(t, int64(0), i.Reserved())
	assert.Equal(t, domain.DefaultLocation, i.Location())
	assert.True(t, i.OwnedBy("shop1"))
	assert.False(t, i.OwnedBy("shop2"))
	assert.False(t, i.OwnedBy(""))
	assert.Equal(t, "p1", i.ProductID())
	assert.Equal(t, "shop1", i.ShopID())
	assert.Equal(t, "i1", i.ID())

	for _, args := range [][3]any{{"", "shop", int64(1)}, {"p", " ", int64(1)}, {"p", "s", int64(-1)}} {
		_, err := domain.NewInventory("id", args[0].(string), args[1].(string), args[2].(int64), now)
		assert.Error(t, err, "%v", args)
	}
	assert.Equal(t, i.Snapshot(), domain.RestoreInventory(i.Snapshot()).Snapshot())
}

func TestReserveReleaseCommitKeepTheTotals(t *testing.T) {
	i := inv(t, 10)
	require.NoError(t, i.Reserve(4, now))
	assert.Equal(t, [2]int64{6, 4}, [2]int64{i.Stock(), i.Reserved()})

	require.NoError(t, i.Release(1, now))
	assert.Equal(t, [2]int64{7, 3}, [2]int64{i.Stock(), i.Reserved()}, "released units are available again")

	require.NoError(t, i.Commit(3, now))
	assert.Equal(t, [2]int64{7, 0}, [2]int64{i.Stock(), i.Reserved()}, "committed units leave the warehouse for good")
}

func TestReserveRefusesMoreThanAvailableAndChangesNothing(t *testing.T) {
	i := inv(t, 3)
	err := i.Reserve(4, now)
	require.Error(t, err)
	assert.Equal(t, domain.CodeInsufficientStock, errs.CodeOf(err))
	assert.Equal(t, errs.KindFailedPrecondition, errs.KindOf(err))
	var short *domain.InsufficientStockError
	require.True(t, errors.As(err, &short))
	assert.Equal(t, "p1", short.ProductID)
	assert.Equal(t, int64(4), short.Requested)
	assert.Equal(t, int64(3), short.Available)
	assert.Equal(t, [2]int64{3, 0}, [2]int64{i.Stock(), i.Reserved()})
	require.NoError(t, i.Reserve(3, now), "exactly the available amount is fine")
	assert.Equal(t, int64(0), i.Stock())
}

func TestQuantitiesMustBePositive(t *testing.T) {
	i := inv(t, 5)
	for _, q := range []int64{0, -1} {
		assert.Equal(t, domain.CodeInvalidQuantity, errs.CodeOf(i.Reserve(q, now)))
		assert.Equal(t, domain.CodeInvalidQuantity, errs.CodeOf(i.Release(q, now)))
		assert.Equal(t, domain.CodeInvalidQuantity, errs.CodeOf(i.Commit(q, now)))
		assert.Equal(t, domain.CodeInvalidQuantity, errs.CodeOf(i.AddStock(q, "", now)))
	}
	assert.False(t, i.CanFulfil(0))
	assert.True(t, i.CanFulfil(5))
	assert.False(t, i.CanFulfil(6))
}

func TestCannotReleaseOrCommitMoreThanReserved(t *testing.T) {
	i := inv(t, 5)
	require.NoError(t, i.Reserve(2, now))
	assert.Equal(t, domain.CodeInvalidState, errs.CodeOf(i.Release(3, now)))
	assert.Equal(t, domain.CodeInvalidState, errs.CodeOf(i.Commit(3, now)))
	assert.Equal(t, [2]int64{3, 2}, [2]int64{i.Stock(), i.Reserved()}, "failed operations change nothing")
}

func TestAddAndSetStock(t *testing.T) {
	i := inv(t, 5)
	require.NoError(t, i.AddStock(5, "  Warehouse 7 ", now))
	assert.Equal(t, int64(10), i.Stock())
	assert.Equal(t, "Warehouse 7", i.Location())
	require.NoError(t, i.AddStock(1, "", now))
	assert.Equal(t, "Warehouse 7", i.Location(), "an empty location keeps the old one")

	require.NoError(t, i.Reserve(4, now))
	require.NoError(t, i.SetStock(20, now))
	assert.Equal(t, [2]int64{20, 4}, [2]int64{i.Stock(), i.Reserved()}, "correcting the shelf count does not touch held units")
	assert.Error(t, i.SetStock(-1, now))
	require.NoError(t, i.SetStock(0, now))
}

func TestReservationLifecycle(t *testing.T) {
	exp := now.Add(15 * time.Minute)
	for name, end := range map[string]func(*domain.Reservation) error{
		"commit":  func(r *domain.Reservation) error { return r.Commit(now) },
		"release": func(r *domain.Reservation) error { return r.Release(now) },
		"expire":  func(r *domain.Reservation) error { return r.Expire(now) },
	} {
		r, err := domain.NewReservation("r1", "o1", "p1", 2, exp, now)
		require.NoError(t, err)
		assert.True(t, r.Active())
		require.NoError(t, end(r), name)
		assert.False(t, r.Active())
		for _, again := range []error{r.Commit(now), r.Release(now), r.Expire(now)} {
			assert.Equal(t, domain.CodeInvalidState, errs.CodeOf(again), name+": a finished hold never changes again")
		}
	}
}

func TestReservationExpiryAndValidation(t *testing.T) {
	r, _ := domain.NewReservation("r1", "o1", "p1", 2, now.Add(time.Minute), now)
	assert.False(t, r.IsExpired(now))
	assert.True(t, r.IsExpired(now.Add(time.Minute)), "expired at the deadline")
	require.NoError(t, r.Commit(now))
	assert.False(t, r.IsExpired(now.Add(time.Hour)), "only active holds expire")

	for _, a := range [][3]any{{"", "p", int64(1)}, {"o", "", int64(1)}, {"o", "p", int64(0)}} {
		_, err := domain.NewReservation("r", a[0].(string), a[1].(string), a[2].(int64), now, now)
		assert.Error(t, err)
	}
	assert.Equal(t, r.Snapshot(), domain.RestoreReservation(r.Snapshot()).Snapshot())
	assert.Equal(t, "o1", r.OrderID())
	assert.Equal(t, "p1", r.ProductID())
	assert.Equal(t, int64(2), r.Quantity())
	assert.Equal(t, domain.ReservationCommitted, r.Status())
	assert.Equal(t, now.Add(time.Minute), r.ExpiresAt())
	assert.Equal(t, "r1", r.ID())
}

func TestInventoryNotFoundAndNotOwner(t *testing.T) {
	assert.Equal(t, errs.KindNotFound, errs.KindOf(domain.ErrInventoryNotFound()))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(domain.ErrNotOwner()))
}
