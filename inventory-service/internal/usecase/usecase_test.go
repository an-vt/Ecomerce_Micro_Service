package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/inventory-service/internal/fake"
	"github.com/an-vt/ecom/inventory-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/saga"
)

var bg = context.Background()

type fx struct {
	w   *fake.World
	clk *clock.Fixed
	set *usecase.Set
}

func newFx() *fx {
	w := fake.NewWorld()
	f := &fx{w: w, clk: clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))}
	f.set = usecase.NewSet(&usecase.Deps{Inventories: w.Inventories(), Reservations: w.Reservations(), Tx: w, Outbox: w,
		Clock: f.clk, IDs: &idgen.Sequence{Prefix: "id-"}, ReservationTTL: 15 * time.Minute})
	return f
}

func (f *fx) seed(t *testing.T, product, shop string, stock int64) {
	t.Helper()
	require.NoError(t, f.set.CreateInventory.Execute(bg, usecase.ProductCreated{ProductID: product, ShopID: shop, Quantity: stock}))
}

func (f *fx) stock(t *testing.T, product string) (int64, int64) {
	t.Helper()
	s, err := f.set.GetStock.Execute(bg, product)
	require.NoError(t, err)
	return s.Stock, s.Reserved
}

func asShop(id string) context.Context {
	return principal.With(bg, principal.Principal{ID: id, Type: principal.TypeShop, Roles: []string{"shop"}})
}

func items(pairs ...any) []saga.Item {
	var out []saga.Item
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, saga.Item{ProductID: pairs[i].(string), Quantity: int64(pairs[i+1].(int))})
	}
	return out
}

// last returns the (type, order) of the most recent reply.
func (f *fx) last() [2]string {
	r := f.w.Replies()
	return r[len(r)-1]
}

// ---- catalog events ----------------------------------------------------------------------------------------------

func TestCreateInventoryNeverOverwritesNewerStock(t *testing.T) {
	f := newFx()
	f.seed(t, "p1", "shop1", 10)
	q := int64(25)
	require.NoError(t, f.set.SyncStock.Execute(bg, usecase.ProductUpdated{ProductID: "p1", ShopID: "shop1", Quantity: &q}))
	f.seed(t, "p1", "shop1", 10) // a redelivered "created" event arrives late
	s, _ := f.stock(t, "p1")
	assert.Equal(t, int64(25), s, "the late created event did not reset the stock")
	assert.Error(t, f.set.CreateInventory.Execute(bg, usecase.ProductCreated{ProductID: "", ShopID: "s", Quantity: 1}))
	assert.Error(t, f.set.CreateInventory.Execute(bg, usecase.ProductCreated{ProductID: "p", ShopID: "s", Quantity: -1}))
}

func TestSyncStockSetsAvailableUnitsButKeepsHeldOnes(t *testing.T) {
	f := newFx()
	f.seed(t, "p1", "shop1", 10)
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o1", Items: items("p1", 4)}))

	q := int64(50)
	require.NoError(t, f.set.SyncStock.Execute(bg, usecase.ProductUpdated{ProductID: "p1", ShopID: "shop1", Quantity: &q}))
	s, r := f.stock(t, "p1")
	assert.Equal(t, [2]int64{50, 4}, [2]int64{s, r})

	require.NoError(t, f.set.SyncStock.Execute(bg, usecase.ProductUpdated{ProductID: "p1", ShopID: "shop1"}), "an update without a quantity is a no-op")
	s, _ = f.stock(t, "p1")
	assert.Equal(t, int64(50), s)
}

func TestSyncStockCreatesTheRecordWhenTheUpdateArrivesBeforeTheCreate(t *testing.T) {
	f := newFx()
	q := int64(7)
	require.NoError(t, f.set.SyncStock.Execute(bg, usecase.ProductUpdated{ProductID: "late", ShopID: "shop1", Quantity: &q}))
	s, _ := f.stock(t, "late")
	assert.Equal(t, int64(7), s)
	neg := int64(-1)
	assert.Error(t, f.set.SyncStock.Execute(bg, usecase.ProductUpdated{ProductID: "p", ShopID: "s", Quantity: &neg}))
}

// ---- shop operations ---------------------------------------------------------------------------------------------

func TestAddStockChecksOwnership(t *testing.T) {
	f := newFx()
	f.seed(t, "p1", "shop1", 10)

	got, err := f.set.AddStock.Execute(asShop("shop1"), usecase.AddStockInput{ProductID: "p1", Amount: 5, Location: "Hall 2"})
	require.NoError(t, err)
	assert.Equal(t, int64(15), got.Stock)
	assert.Equal(t, "Hall 2", got.Location)

	_, err = f.set.AddStock.Execute(asShop("shop2"), usecase.AddStockInput{ProductID: "p1", Amount: 5})
	assert.Equal(t, domain.CodeNotOwner, errs.CodeOf(err))
	_, err = f.set.AddStock.Execute(asShop("shop1"), usecase.AddStockInput{ProductID: "ghost", Amount: 5})
	assert.Equal(t, domain.CodeInventoryNotFound, errs.CodeOf(err))
	_, err = f.set.AddStock.Execute(asShop("shop1"), usecase.AddStockInput{ProductID: "p1", Amount: 0})
	assert.Equal(t, domain.CodeInvalidQuantity, errs.CodeOf(err))
	_, err = f.set.AddStock.Execute(bg, usecase.AddStockInput{ProductID: "p1", Amount: 1})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = f.set.AddStock.Execute(principal.With(bg, principal.Principal{ID: "u1", Type: principal.TypeUser}), usecase.AddStockInput{ProductID: "p1", Amount: 1})
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
	s, _ := f.stock(t, "p1")
	assert.Equal(t, int64(15), s, "rejected calls changed nothing")
}

func TestGetStockUnknownProduct(t *testing.T) {
	_, err := newFx().set.GetStock.Execute(bg, "ghost")
	assert.Equal(t, domain.CodeInventoryNotFound, errs.CodeOf(err))
}

func TestCheckStockReportsEveryShortage(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 5)
	f.seed(t, "b", "s", 1)

	ok, short, err := f.set.CheckStock.Execute(bg, []usecase.Line{{"a", 5}, {"b", 1}})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Empty(t, short)

	ok, short, err = f.set.CheckStock.Execute(bg, []usecase.Line{{"a", 3}, {"a", 3}, {"b", 2}, {"ghost", 1}})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, []usecase.Shortage{{ProductID: "a", Requested: 6, Available: 5}, {ProductID: "b", Requested: 2, Available: 1},
		{ProductID: "ghost", Requested: 1, Available: 0}}, short, "lines of one product are added up; unknown products have none")

	ok, _, err = f.set.CheckStock.Execute(bg, nil)
	require.NoError(t, err)
	assert.True(t, ok)
	_, _, err = f.set.CheckStock.Execute(bg, []usecase.Line{{"a", 0}})
	assert.Equal(t, domain.CodeInvalidQuantity, errs.CodeOf(err))
	_, _, err = f.set.CheckStock.Execute(bg, make([]usecase.Line, 101))
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
}

// ---- saga: reserve -----------------------------------------------------------------------------------------------

func TestReserveHoldsStockAndRepliesInTheSameTransaction(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	f.seed(t, "b", "s", 5)
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o1", Items: items("a", 3, "b", 2, "a", 1)}))

	sa, ra := f.stock(t, "a")
	sb, rb := f.stock(t, "b")
	assert.Equal(t, [4]int64{6, 4, 3, 2}, [4]int64{sa, ra, sb, rb}, "lines of the same product are merged")
	assert.Equal(t, [2]string{saga.ReplyStockReserved, "o1"}, f.last())
	rs, _ := f.w.Reservations().ForOrder(bg, "o1")
	require.Len(t, rs, 2)
	assert.Equal(t, f.clk.Now().Add(15*time.Minute), rs[0].ExpiresAt())
}

func TestReserveIsAllOrNothing(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	f.seed(t, "b", "s", 1)
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o1", Items: items("a", 3, "b", 2)}))

	assert.Equal(t, [2]string{saga.ReplyStockReservationFailed, "o1"}, f.last())
	var reply saga.StockReservationFailed
	require.NoError(t, f.w.Reply(0, &reply))
	assert.Equal(t, saga.ReasonInsufficientStock, reply.Reason)
	assert.Equal(t, "b", reply.ProductID, "the reply names the product that ran short")
	sa, ra := f.stock(t, "a")
	assert.Equal(t, [2]int64{10, 0}, [2]int64{sa, ra}, "the available product was not touched")
	rs, _ := f.w.Reservations().ForOrder(bg, "o1")
	assert.Empty(t, rs)
}

func TestReserveUnknownProductAndBadInput(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o1", Items: items("a", 1, "ghost", 1)}))
	var reply saga.StockReservationFailed
	require.NoError(t, f.w.Reply(0, &reply))
	assert.Equal(t, saga.ReasonUnknownProduct, reply.Reason)
	assert.Equal(t, "ghost", reply.ProductID)

	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o2", Items: nil}))
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o3", Items: items("a", 0)}))
	for i := 1; i < 3; i++ {
		assert.Equal(t, saga.ReplyStockReservationFailed, f.w.Replies()[i][0], "malformed commands get a failure reply, not an exception")
	}
	s, r := f.stock(t, "a")
	assert.Equal(t, [2]int64{10, 0}, [2]int64{s, r})
}

func TestRedeliveredReserveChangesNothingAndConfirms(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	cmd := saga.ReserveStock{OrderID: "o1", Items: items("a", 4)}
	require.NoError(t, f.set.ReserveStock.Execute(bg, cmd))
	require.NoError(t, f.set.ReserveStock.Execute(bg, cmd))
	s, r := f.stock(t, "a")
	assert.Equal(t, [2]int64{6, 4}, [2]int64{s, r}, "stock was held once")
	assert.Equal(t, []string{saga.ReplyStockReserved, saga.ReplyStockReserved}, []string{f.w.Replies()[0][0], f.w.Replies()[1][0]})
}

func TestReserveAfterTheOrderWasReleasedIsRefused(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	cmd := saga.ReserveStock{OrderID: "o1", Items: items("a", 4)}
	require.NoError(t, f.set.ReserveStock.Execute(bg, cmd))
	require.NoError(t, f.set.ReleaseStock.Execute(bg, saga.ReleaseStock{OrderID: "o1"}))
	require.NoError(t, f.set.ReserveStock.Execute(bg, cmd))
	assert.Equal(t, saga.ReplyStockReservationFailed, f.last()[0], "a released order cannot silently take stock again")
	s, r := f.stock(t, "a")
	assert.Equal(t, [2]int64{10, 0}, [2]int64{s, r})
}

func TestReserveIsAtomicWithItsReply(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	f.w.FailOutbox = errors.New("outbox unavailable")
	err := f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o1", Items: items("a", 4)})
	require.Error(t, err)
	f.w.FailOutbox = nil
	s, r := f.stock(t, "a")
	assert.Equal(t, [2]int64{10, 0}, [2]int64{s, r}, "no reply means no hold: the retry starts clean")
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o1", Items: items("a", 4)}))
	s, r = f.stock(t, "a")
	assert.Equal(t, [2]int64{6, 4}, [2]int64{s, r})
}

// ---- saga: release / commit ----------------------------------------------------------------------------------------

func TestReleaseGivesStockBackOnceAndAlwaysReplies(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o1", Items: items("a", 4)}))

	require.NoError(t, f.set.ReleaseStock.Execute(bg, saga.ReleaseStock{OrderID: "o1"}))
	s, r := f.stock(t, "a")
	assert.Equal(t, [2]int64{10, 0}, [2]int64{s, r})
	require.NoError(t, f.set.ReleaseStock.Execute(bg, saga.ReleaseStock{OrderID: "o1"}), "repeating a compensation is harmless")
	require.NoError(t, f.set.ReleaseStock.Execute(bg, saga.ReleaseStock{OrderID: "never-reserved"}))
	s, r = f.stock(t, "a")
	assert.Equal(t, [2]int64{10, 0}, [2]int64{s, r}, "released exactly once")
	replies := f.w.Replies()
	assert.Equal(t, []string{saga.ReplyStockReleased, saga.ReplyStockReleased, saga.ReplyStockReleased}, []string{replies[1][0], replies[2][0], replies[3][0]})
}

func TestCommitSellsTheHeldStock(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o1", Items: items("a", 4)}))
	require.NoError(t, f.set.CommitStock.Execute(bg, saga.CommitStock{OrderID: "o1"}))
	s, r := f.stock(t, "a")
	assert.Equal(t, [2]int64{6, 0}, [2]int64{s, r})
	assert.Equal(t, saga.ReplyStockCommitted, f.last()[0])

	require.NoError(t, f.set.CommitStock.Execute(bg, saga.CommitStock{OrderID: "o1"}), "a repeat is confirmed again")
	s, _ = f.stock(t, "a")
	assert.Equal(t, int64(6), s, "...without selling twice")
}

func TestCommitOfAnUnheldOrderIsAPermanentFailure(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	err := f.set.CommitStock.Execute(bg, saga.CommitStock{OrderID: "ghost"})
	require.Error(t, err)
	assert.True(t, errs.IsPermanent(err), "goes to the dead-letter topic for a human instead of retrying forever")
	assert.Equal(t, domain.CodeNothingReserved, errs.CodeOf(err))

	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o1", Items: items("a", 1)}))
	require.NoError(t, f.set.ReleaseStock.Execute(bg, saga.ReleaseStock{OrderID: "o1"}))
	err = f.set.CommitStock.Execute(bg, saga.CommitStock{OrderID: "o1"})
	assert.Equal(t, domain.CodeInvalidState, errs.CodeOf(err), "stock that was given back cannot be sold")
	s, r := f.stock(t, "a")
	assert.Equal(t, [2]int64{10, 0}, [2]int64{s, r})
}

// ---- expiry --------------------------------------------------------------------------------------------------------

func TestExpiryReturnsOnlyOverdueHolds(t *testing.T) {
	f := newFx()
	f.seed(t, "a", "s", 10)
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "old", Items: items("a", 3)}))
	f.clk.Advance(10 * time.Minute)
	require.NoError(t, f.set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "new", Items: items("a", 2)}))

	n, err := f.set.ExpireReservations.Execute(bg, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "nothing is overdue yet")

	f.clk.Advance(6 * time.Minute) // "old" is 16 min old, "new" 6
	n, err = f.set.ExpireReservations.Execute(bg, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	s, r := f.stock(t, "a")
	assert.Equal(t, [2]int64{8, 2}, [2]int64{s, r}, "only the overdue order gave its units back")

	old, _ := f.w.Reservations().ForOrder(bg, "old")
	assert.Equal(t, domain.ReservationExpired, old[0].Status())
	fresh, _ := f.w.Reservations().ForOrder(bg, "new")
	assert.Equal(t, domain.ReservationReserved, fresh[0].Status())

	n, err = f.set.ExpireReservations.Execute(bg, 10)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "expiring is idempotent")
	err = f.set.CommitStock.Execute(bg, saga.CommitStock{OrderID: "old"})
	assert.Equal(t, domain.CodeInvalidState, errs.CodeOf(err), "an expired hold cannot be committed")
}

func TestDefaultReservationTTL(t *testing.T) {
	w := fake.NewWorld()
	clk := clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	set := usecase.NewSet(&usecase.Deps{Inventories: w.Inventories(), Reservations: w.Reservations(), Tx: w, Outbox: w, Clock: clk, IDs: &idgen.Sequence{Prefix: "x-"}})
	require.NoError(t, set.CreateInventory.Execute(bg, usecase.ProductCreated{ProductID: "a", ShopID: "s", Quantity: 5}))
	require.NoError(t, set.ReserveStock.Execute(bg, saga.ReserveStock{OrderID: "o", Items: items("a", 1)}))
	rs, _ := w.Reservations().ForOrder(bg, "o")
	assert.Equal(t, clk.Now().Add(15*time.Minute), rs[0].ExpiresAt())
}
