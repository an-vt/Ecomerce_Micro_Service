package usecase_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/order-service/internal/fake"
	"github.com/an-vt/ecom/order-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/saga"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// metrics records what the orchestration reports.
type metrics struct {
	started, stale int
	finished       []domain.SagaState
	reasons        []string
	compensations  []string
	timeouts       []domain.SagaState
}

func (m *metrics) Started()    { m.started++ }
func (m *metrics) StaleReply() { m.stale++ }
func (m *metrics) Finished(s domain.SagaState, reason string, _ time.Duration) {
	m.finished = append(m.finished, s)
	m.reasons = append(m.reasons, reason)
}
func (m *metrics) Compensation(c string)          { m.compensations = append(m.compensations, c) }
func (m *metrics) StepTimeout(s domain.SagaState) { m.timeouts = append(m.timeouts, s) }

type rig struct {
	uc  *usecase.Set
	w   *fake.World
	cat *fake.Catalog
	dis *fake.Discounts
	clk *clock.Fixed
	m   *metrics
}

func newRig() *rig {
	w := fake.NewWorld()
	cat := &fake.Catalog{Products_: map[string]domain.Product{
		"p1": {ID: "p1", ShopID: "s1", Name: "Shirt", Price: 100000, IsPublished: true},
		"p2": {ID: "p2", ShopID: "s2", Name: "Hat", Price: 50000, IsPublished: true},
		"p3": {ID: "p3", ShopID: "s1", Name: "Draft", Price: 1, IsPublished: false},
	}}
	dis := &fake.Discounts{Percent: map[string]int64{"s1/SALE10": 10}}
	clk := clock.NewFixed(now)
	m := &metrics{}
	uc := usecase.NewSet(&usecase.Deps{Orders: w.Orders(), Sagas: w.Sagas(), Keys: w.Keys(), Catalog: cat, Discounts: dis, Tx: w, Outbox: w, Clock: clk,
		IDs: idgen.UUID{}, Metrics: m, Timeouts: domain.Timeouts{Step: 30 * time.Second, Compensation: 20 * time.Second}, FeeShip: 5000})
	return &rig{uc: uc, w: w, cat: cat, dis: dis, clk: clk, m: m}
}

func buyer(id string) context.Context {
	return principal.With(context.Background(), principal.Principal{ID: id, Type: principal.TypeUser})
}

func shop(id string) context.Context {
	return principal.With(context.Background(), principal.Principal{ID: id, Type: principal.TypeShop})
}

func shopOrders(code string) []usecase.ShopOrder {
	return []usecase.ShopOrder{
		{ShopID: "s1", DiscountCode: code, Items: []usecase.ItemInput{{ProductID: "p1", Quantity: 2}}},
		{ShopID: "s2", Items: []usecase.ItemInput{{ProductID: "p2", Quantity: 1}}}}
}

func placeIn(key, code string) usecase.PlaceInput {
	return usecase.PlaceInput{ShopOrders: shopOrders(code), Shipping: domain.Shipping{Name: "An", Address: "1 Main St", City: "HCMC"},
		PaymentMethod: "card", PaymentToken: "tok_ok", IdempotencyKey: key}
}

func (r *rig) place(t *testing.T, key, code string) string {
	t.Helper()
	p, err := r.uc.PlaceOrder.Execute(buyer("alice"), placeIn(key, code))
	require.NoError(t, err)
	return p.OrderID
}

func (r *rig) reply(t *testing.T, m saga.Message) {
	t.Helper()
	r.clk.Advance(time.Second)
	require.NoError(t, r.uc.HandleSagaReply.Execute(context.Background(), m))
}

func (r *rig) order(t *testing.T, id string) domain.OrderSnapshot {
	t.Helper()
	s, ok := r.w.Order(id)
	require.True(t, ok)
	return s
}

// ---- review -------------------------------------------------------------------------------------------------------

func TestReviewPricesFromTheCatalogAndPreviewsTheDiscount(t *testing.T) {
	r := newRig()
	rv, err := r.uc.ReviewCheckout.Execute(buyer("alice"), shopOrders("SALE10"))
	require.NoError(t, err)

	require.Len(t, rv.Shops, 2)
	assert.EqualValues(t, 200000, rv.Shops[0].Subtotal)
	assert.EqualValues(t, 20000, rv.Shops[0].Discount)
	assert.EqualValues(t, 180000, rv.Shops[0].Total)
	assert.Equal(t, "SALE10", rv.Shops[0].DiscountCode)
	assert.Equal(t, "Shirt", rv.Shops[0].Items[0].Name, "names and prices come from the catalog, never from the client")
	assert.EqualValues(t, 50000, rv.Shops[1].Total)
	assert.EqualValues(t, 250000, rv.TotalPrice)
	assert.EqualValues(t, 20000, rv.TotalDiscount)
	assert.EqualValues(t, 5000, rv.FeeShip)
	assert.EqualValues(t, 235000, rv.TotalCheckout)
	assert.Equal(t, []string{"s1/SALE10"}, r.dis.Calls, "only shops with a code are asked")
	assert.Empty(t, r.w.OrderIDs(), "a review writes nothing")
	assert.Empty(t, r.w.Types())
}

func TestReviewWithoutCodesNeverCallsTheDiscountService(t *testing.T) {
	r := newRig()
	rv, err := r.uc.ReviewCheckout.Execute(buyer("alice"), shopOrders(""))
	require.NoError(t, err)
	assert.EqualValues(t, 255000, rv.TotalCheckout)
	assert.Empty(t, r.dis.Calls)
}

func TestReviewRefusals(t *testing.T) {
	r := newRig()
	bad := func(mut func([]usecase.ShopOrder) []usecase.ShopOrder) []usecase.ShopOrder {
		return mut(shopOrders(""))
	}
	for name, tc := range map[string]struct {
		orders []usecase.ShopOrder
		kind   errs.Kind
		code   string
	}{
		"no shops":           {nil, errs.KindInvalidArgument, errs.CodeValidationFailed},
		"shop without items": {bad(func(o []usecase.ShopOrder) []usecase.ShopOrder { o[0].Items = nil; return o }), errs.KindInvalidArgument, errs.CodeValidationFailed},
		"no shop id":         {bad(func(o []usecase.ShopOrder) []usecase.ShopOrder { o[0].ShopID = ""; return o }), errs.KindInvalidArgument, errs.CodeValidationFailed},
		"shop twice":         {bad(func(o []usecase.ShopOrder) []usecase.ShopOrder { o[1].ShopID = "s1"; return o }), errs.KindInvalidArgument, errs.CodeValidationFailed},
		"zero quantity":      {bad(func(o []usecase.ShopOrder) []usecase.ShopOrder { o[0].Items[0].Quantity = 0; return o }), errs.KindInvalidArgument, errs.CodeValidationFailed},
		"product twice":      {bad(func(o []usecase.ShopOrder) []usecase.ShopOrder { o[1].Items[0].ProductID = "p1"; return o }), errs.KindInvalidArgument, errs.CodeValidationFailed},
		"unknown product":    {bad(func(o []usecase.ShopOrder) []usecase.ShopOrder { o[0].Items[0].ProductID = "ghost"; return o }), errs.KindNotFound, domain.CodeProductUnavailable},
		"unpublished":        {bad(func(o []usecase.ShopOrder) []usecase.ShopOrder { o[0].Items[0].ProductID = "p3"; return o }), errs.KindNotFound, domain.CodeProductUnavailable},
		"wrong shop":         {bad(func(o []usecase.ShopOrder) []usecase.ShopOrder { o[0].ShopID = "s2"; o[1].ShopID = "s1"; return o }), errs.KindInvalidArgument, domain.CodeShopMismatch},
	} {
		_, err := r.uc.ReviewCheckout.Execute(buyer("alice"), tc.orders)
		require.Error(t, err, name)
		assert.Equal(t, tc.kind, errs.KindOf(err), name)
		assert.Equal(t, tc.code, errs.CodeOf(err), name)
	}

	tooMany := make([]usecase.ShopOrder, usecase.MaxShops+1)
	for i := range tooMany {
		tooMany[i] = usecase.ShopOrder{ShopID: string(rune('a' + i)), Items: []usecase.ItemInput{{ProductID: "p1", Quantity: 1}}}
	}
	_, err := r.uc.ReviewCheckout.Execute(buyer("alice"), tooMany)
	assert.Error(t, err)
	manyLines := usecase.ShopOrder{ShopID: "s1"}
	for i := 0; i <= usecase.MaxLines; i++ {
		manyLines.Items = append(manyLines.Items, usecase.ItemInput{ProductID: "p" + string(rune('A'+i%26)) + string(rune('a'+i/26)), Quantity: 1})
	}
	_, err = r.uc.ReviewCheckout.Execute(buyer("alice"), []usecase.ShopOrder{manyLines})
	assert.Error(t, err)
}

func TestReviewNeedsABuyer(t *testing.T) {
	r := newRig()
	_, err := r.uc.ReviewCheckout.Execute(context.Background(), shopOrders(""))
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = r.uc.ReviewCheckout.Execute(shop("s1"), shopOrders(""))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err), "shops do not buy")
	_, err = r.uc.PlaceOrder.Execute(shop("s1"), placeIn("k", ""))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
	_, err = r.uc.GetOrder.Execute(context.Background(), "x")
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = r.uc.ListOrders.Execute(shop("s1"), 1, 10)
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
}

func TestReviewPassesDependencyFailuresThrough(t *testing.T) {
	r := newRig()
	r.cat.Err = errs.Unavailable("X", "catalog down")
	_, err := r.uc.ReviewCheckout.Execute(buyer("alice"), shopOrders(""))
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err), "no catalog, no guessed prices")
	r.cat.Err = nil
	r.dis.Err = errs.FailedPrecondition("DISCOUNT_EXPIRED", "expired")
	_, err = r.uc.ReviewCheckout.Execute(buyer("alice"), shopOrders("SALE10"))
	assert.Equal(t, "DISCOUNT_EXPIRED", errs.CodeOf(err))
	r.dis.Err = nil
	_, err = r.uc.ReviewCheckout.Execute(buyer("alice"), shopOrders("NOPE"))
	assert.Equal(t, "DISCOUNT_NOT_FOUND", errs.CodeOf(err))
}

// ---- place order --------------------------------------------------------------------------------------------------

func TestPlaceOrderStartsTheSagaInOneTransaction(t *testing.T) {
	r := newRig()
	p, err := r.uc.PlaceOrder.Execute(buyer("alice"), placeIn("key-1", "SALE10"))
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, p.Status)

	o := r.order(t, p.OrderID)
	assert.Equal(t, "alice", o.UserID)
	assert.EqualValues(t, 250000, o.TotalPrice)
	assert.EqualValues(t, 255000, o.TotalCheckout, "the discount is subtracted later, from what discount-service really applied")
	assert.Equal(t, []domain.DiscountRef{{ShopID: "s1", Code: "SALE10"}}, o.Discounts)
	assert.Equal(t, "tok_ok", o.PaymentToken)
	sg, ok := r.w.Saga(p.OrderID)
	require.True(t, ok)
	assert.Equal(t, domain.StateReservingStock, sg.State)
	assert.True(t, sg.HasDiscounts)
	assert.Equal(t, now.Add(30*time.Second), sg.StepDeadline)

	assert.Equal(t, []string{saga.CmdReserveStock}, r.w.Types())
	assert.Equal(t, []string{saga.TopicInventoryCommands}, r.w.Topics())
	assert.Equal(t, p.OrderID, r.w.Key(0), "keyed by the order id")
	var cmd saga.ReserveStock
	require.NoError(t, r.w.Decode(0, &cmd))
	assert.Equal(t, []saga.Item{{ProductID: "p1", ShopID: "s1", Quantity: 2, Price: 100000}, {ProductID: "p2", ShopID: "s2", Quantity: 1, Price: 50000}}, cmd.Items)
	assert.Equal(t, 1, r.m.started)
}

// Acceptance #2
func TestTheSameKeyAndRequestReturnsTheSameOrder(t *testing.T) {
	r := newRig()
	first := r.place(t, "key-1", "SALE10")
	calls := r.cat.Calls

	for i := 0; i < 3; i++ {
		again, err := r.uc.PlaceOrder.Execute(buyer("alice"), placeIn("key-1", "SALE10"))
		require.NoError(t, err)
		assert.Equal(t, first, again.OrderID)
	}
	assert.Len(t, r.w.OrderIDs(), 1, "one order")
	assert.Len(t, r.w.Types(), 1, "one ReserveStock")
	assert.Equal(t, calls, r.cat.Calls, "a replay does not even re-price")

	// progress is visible to the replaying client
	r.reply(t, saga.StockReserved{OrderID: first})
	again, err := r.uc.PlaceOrder.Execute(buyer("alice"), placeIn("key-1", "SALE10"))
	require.NoError(t, err)
	assert.Equal(t, domain.StatusStockReserved, again.Status)
}

func TestTheSameKeyWithADifferentRequestIsAConflict(t *testing.T) {
	r := newRig()
	r.place(t, "key-1", "SALE10")
	for name, mut := range map[string]func(*usecase.PlaceInput){
		"other code":     func(in *usecase.PlaceInput) { in.ShopOrders[0].DiscountCode = "" },
		"other quantity": func(in *usecase.PlaceInput) { in.ShopOrders[0].Items[0].Quantity = 3 },
		"other token":    func(in *usecase.PlaceInput) { in.PaymentToken = "tok_fail" },
		"other address":  func(in *usecase.PlaceInput) { in.Shipping.Address = "elsewhere" },
	} {
		in := placeIn("key-1", "SALE10")
		mut(&in)
		_, err := r.uc.PlaceOrder.Execute(buyer("alice"), in)
		require.Error(t, err, name)
		assert.Equal(t, domain.CodeIdempotencyConflict, errs.CodeOf(err), name)
		assert.Equal(t, errs.KindAborted, errs.KindOf(err))
	}
	assert.Len(t, r.w.OrderIDs(), 1)
}

func TestTheOrderOfTheListsDoesNotChangeTheRequest(t *testing.T) {
	r := newRig()
	first := r.place(t, "key-1", "SALE10")
	in := placeIn("key-1", " sale10 ")
	in.ShopOrders[0], in.ShopOrders[1] = in.ShopOrders[1], in.ShopOrders[0]
	again, err := r.uc.PlaceOrder.Execute(buyer("alice"), in)
	require.NoError(t, err, "same content, any order, any code case")
	assert.Equal(t, first, again.OrderID)
}

func TestKeysAreScopedToTheBuyer(t *testing.T) {
	r := newRig()
	a := r.place(t, "shared-key", "")
	b, err := r.uc.PlaceOrder.Execute(buyer("bob"), placeIn("shared-key", ""))
	require.NoError(t, err)
	assert.NotEqual(t, a, b.OrderID, "another buyer's key is another key")
}

func TestPlaceOrderRefusals(t *testing.T) {
	r := newRig()
	for _, key := range []string{"", "   ", string(make([]byte, usecase.MaxKeyLength+1))} {
		_, err := r.uc.PlaceOrder.Execute(buyer("alice"), placeIn(key, ""))
		require.Error(t, err)
		assert.Equal(t, domain.CodeIdempotencyKey, errs.CodeOf(err))
	}
	noAddress := placeIn("k1", "")
	noAddress.Shipping.Address = ""
	noToken := placeIn("k2", "")
	noToken.PaymentToken = ""
	for name, in := range map[string]usecase.PlaceInput{"no address": noAddress, "no token": noToken} {
		_, err := r.uc.PlaceOrder.Execute(buyer("alice"), in)
		require.Error(t, err, name)
		assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err), name)
	}
	_, err := r.uc.PlaceOrder.Execute(buyer("alice"), usecase.PlaceInput{IdempotencyKey: "k3"})
	assert.Error(t, err, "no items")
	assert.Empty(t, r.w.OrderIDs(), "nothing was stored by any refused request")
	assert.Empty(t, r.w.Types())
	assert.Zero(t, r.m.started)
}

func TestAFailedPlaceOrderLeavesNothingBehind(t *testing.T) {
	r := newRig()
	r.w.FailOutbox = errors.New("outbox down")
	_, err := r.uc.PlaceOrder.Execute(buyer("alice"), placeIn("key-1", ""))
	require.Error(t, err)
	assert.Empty(t, r.w.OrderIDs(), "no order without its first command")
	r.w.FailOutbox = nil
	p, err := r.uc.PlaceOrder.Execute(buyer("alice"), placeIn("key-1", ""))
	require.NoError(t, err, "the key was not consumed by the failed attempt")
	assert.Equal(t, domain.StatusPending, p.Status)
}

// lostRace answers Find with "nothing" the first time, then claims nothing: another request took the key in between.
type lostRace struct {
	domain.IdempotencyRepository
	finds int
	rec   *domain.IdempotencyRecord
}

func (l *lostRace) Find(ctx context.Context, user, key string) (*domain.IdempotencyRecord, error) {
	l.finds++
	if l.finds == 1 {
		return nil, nil
	}
	return l.rec, nil
}

func (l *lostRace) Claim(context.Context, domain.IdempotencyRecord) (bool, error) { return false, nil }

func TestARequestThatLosesTheClaimReplaysTheWinner(t *testing.T) {
	r := newRig()
	winner := r.place(t, "key-1", "")
	rec, _ := r.w.Keys().Find(context.Background(), "alice", "key-1")
	race := &lostRace{IdempotencyRepository: r.w.Keys(), rec: rec}
	uc := usecase.NewSet(&usecase.Deps{Orders: r.w.Orders(), Sagas: r.w.Sagas(), Keys: race, Catalog: r.cat, Discounts: r.dis, Tx: r.w, Outbox: r.w,
		Clock: r.clk, IDs: idgen.UUID{}, FeeShip: 5000})

	got, err := uc.PlaceOrder.Execute(buyer("alice"), placeIn("key-1", ""))
	require.NoError(t, err)
	assert.Equal(t, winner, got.OrderID, "the loser answers with the winner's order")
	assert.Len(t, r.w.OrderIDs(), 1, "and wrote nothing of its own")

	race = &lostRace{IdempotencyRepository: r.w.Keys(), rec: nil} // the winner rolled back
	uc = usecase.NewSet(&usecase.Deps{Orders: r.w.Orders(), Sagas: r.w.Sagas(), Keys: race, Catalog: r.cat, Discounts: r.dis, Tx: r.w, Outbox: r.w,
		Clock: r.clk, IDs: idgen.UUID{}, FeeShip: 5000})
	_, err = uc.PlaceOrder.Execute(buyer("alice"), placeIn("key-2", ""))
	assert.Equal(t, domain.CodeIdempotencyConflict, errs.CodeOf(err), "retry later")

	race = &lostRace{IdempotencyRepository: r.w.Keys(), rec: &domain.IdempotencyRecord{UserID: "alice", Key: "key-3", RequestHash: "other", OrderID: winner}}
	uc = usecase.NewSet(&usecase.Deps{Orders: r.w.Orders(), Sagas: r.w.Sagas(), Keys: race, Catalog: r.cat, Discounts: r.dis, Tx: r.w, Outbox: r.w,
		Clock: r.clk, IDs: idgen.UUID{}, FeeShip: 5000})
	_, err = uc.PlaceOrder.Execute(buyer("alice"), placeIn("key-3", ""))
	assert.Equal(t, domain.CodeIdempotencyConflict, errs.CodeOf(err), "the winner had a different request")
}

// ---- saga through the use cases -----------------------------------------------------------------------------------

func TestHappyPathEndToEnd(t *testing.T) {
	r := newRig()
	id := r.place(t, "key-1", "SALE10")

	r.reply(t, saga.StockReserved{OrderID: id})
	assert.Equal(t, domain.StatusStockReserved, r.order(t, id).Status)
	var apply saga.ApplyDiscounts
	require.NoError(t, r.w.Decode(1, &apply))
	assert.Equal(t, "alice", apply.UserID)
	assert.Equal(t, []saga.DiscountRequest{{ShopID: "s1", Code: "SALE10"}}, apply.Discounts)

	r.reply(t, saga.DiscountsApplied{OrderID: id, TotalDiscount: 20000})
	o := r.order(t, id)
	assert.Equal(t, domain.StatusPaymentProcessing, o.Status)
	assert.EqualValues(t, 20000, o.TotalDiscount)
	assert.EqualValues(t, 235000, o.TotalCheckout)
	var pay saga.ProcessPayment
	require.NoError(t, r.w.Decode(2, &pay))
	assert.EqualValues(t, 235000, pay.Amount, "charged: items - what discount-service applied + shipping")
	assert.Equal(t, "tok_ok", pay.Token)
	assert.Equal(t, "card", pay.Method)

	r.reply(t, saga.PaymentCompleted{OrderID: id, PaymentID: "pay-1"})
	r.reply(t, saga.StockCommitted{OrderID: id})

	assert.Equal(t, domain.StatusConfirmed, r.order(t, id).Status)
	sg, _ := r.w.Saga(id)
	assert.Equal(t, domain.StateCompleted, sg.State)
	assert.Equal(t, []string{saga.CmdReserveStock, saga.CmdApplyDiscounts, saga.CmdProcessPayment, saga.CmdCommitStock, "order.order.confirmed"}, r.w.Types())
	assert.Equal(t, []string{saga.TopicInventoryCommands, saga.TopicDiscountCommands, saga.TopicPaymentCommands, saga.TopicInventoryCommands, domain.TopicConfirmed}, r.w.Topics())

	var ev domain.OrderConfirmed
	require.NoError(t, r.w.Decode(4, &ev))
	assert.Equal(t, domain.OrderConfirmed{OrderID: id, UserID: "alice", TotalCheckout: 235000,
		Items: []domain.ConfirmedItem{{ProductID: "p1", Quantity: 2}, {ProductID: "p2", Quantity: 1}}}, ev, "what cart-service needs to empty the bought lines")
	assert.Equal(t, []domain.SagaState{domain.StateCompleted}, r.m.finished)
	assert.Empty(t, r.m.compensations)
}

func TestPaymentFailureCompensatesEverything(t *testing.T) {
	r := newRig()
	id := r.place(t, "key-1", "SALE10")
	r.reply(t, saga.StockReserved{OrderID: id})
	r.reply(t, saga.DiscountsApplied{OrderID: id, TotalDiscount: 20000})
	r.reply(t, saga.PaymentFailed{OrderID: id, Reason: saga.ReasonCardDeclined})

	assert.Equal(t, domain.StatusCancelling, r.order(t, id).Status)
	assert.Equal(t, []string{saga.CmdReserveStock, saga.CmdApplyDiscounts, saga.CmdProcessPayment, saga.CmdReleaseDiscount, saga.CmdReleaseStock}, r.w.Types())
	assert.Equal(t, []string{saga.TopicDiscountCommands, saga.TopicInventoryCommands}, r.w.Topics()[3:], "each undo goes to the participant that did the step")
	assert.Equal(t, []string{saga.CmdReleaseDiscount, saga.CmdReleaseStock}, r.m.compensations)

	r.reply(t, saga.DiscountsReleased{OrderID: id})
	assert.Equal(t, domain.StatusCancelling, r.order(t, id).Status, "waits for every acknowledgement")
	r.reply(t, saga.StockReleased{OrderID: id})

	o := r.order(t, id)
	assert.Equal(t, domain.StatusCancelled, o.Status)
	assert.Equal(t, saga.ReasonCardDeclined, o.CancelReason)
	types := r.w.Types()
	assert.Equal(t, "order.order.cancelled", types[len(types)-1])
	var ev domain.OrderCancelled
	require.NoError(t, r.w.Decode(len(types)-1, &ev))
	assert.Equal(t, domain.OrderCancelled{OrderID: id, UserID: "alice", Reason: saga.ReasonCardDeclined}, ev)
	assert.Equal(t, []domain.SagaState{domain.StateFailed}, r.m.finished)
	assert.Equal(t, []string{saga.ReasonCardDeclined}, r.m.reasons)
}

func TestOutOfStockCancelsAtOnce(t *testing.T) {
	r := newRig()
	id := r.place(t, "key-1", "")
	r.reply(t, saga.StockReservationFailed{OrderID: id, Reason: saga.ReasonInsufficientStock, ProductID: "p1"})
	o := r.order(t, id)
	assert.Equal(t, domain.StatusCancelled, o.Status)
	assert.Equal(t, saga.ReasonInsufficientStock, o.CancelReason)
	assert.Equal(t, []string{saga.CmdReserveStock, "order.order.cancelled"}, r.w.Types(), "nothing was held, so nothing is undone")
}

// Acceptance #3
func TestAReplyThatNoLongerFitsIsIgnoredAndCounted(t *testing.T) {
	r := newRig()
	id := r.place(t, "key-1", "")
	r.clk.Advance(time.Minute)
	n, err := r.uc.SweepSagaTimeouts.Execute(context.Background(), 10)
	require.NoError(t, err)
	require.Equal(t, 1, n, "step 1 timed out: ReleaseStock was sent")
	before := len(r.w.Types())
	sagaBefore, _ := r.w.Saga(id)

	r.reply(t, saga.StockReserved{OrderID: id}) // the answer arrives after the timeout
	assert.Equal(t, 1, r.m.stale, "saga_stale_replies_total + 1")
	assert.Equal(t, before, len(r.w.Types()), "nothing was sent")
	sagaAfter, _ := r.w.Saga(id)
	assert.Equal(t, sagaBefore, sagaAfter, "nothing was changed")
	assert.Equal(t, domain.StatusCancelling, r.order(t, id).Status)
}

func TestAReplyForAnUnknownOrderIsAPermanentFailure(t *testing.T) {
	r := newRig()
	for _, id := range []string{"00000000-0000-4000-8000-00000000dead", "e2e-order-1793304999997133000", ""} {
		err := r.uc.HandleSagaReply.Execute(context.Background(), saga.StockReserved{OrderID: id})
		require.Error(t, err, id)
		assert.True(t, errs.IsPermanent(err), "%q goes to the dead-letter topic, not into a retry loop", id)
	}
}

func TestAReplyAndItsEffectsCommitTogether(t *testing.T) {
	r := newRig()
	id := r.place(t, "key-1", "")
	r.w.FailOutbox = errors.New("outbox down")
	err := r.uc.HandleSagaReply.Execute(context.Background(), saga.StockReserved{OrderID: id})
	require.Error(t, err)
	sg, _ := r.w.Saga(id)
	assert.Equal(t, domain.StateReservingStock, sg.State, "the saga did not advance without its next command")
	assert.Equal(t, domain.StatusPending, r.order(t, id).Status)
	r.w.FailOutbox = nil
	r.reply(t, saga.StockReserved{OrderID: id})
	assert.Equal(t, domain.StatusPaymentProcessing, r.order(t, id).Status, "the redelivery works")
}

// ---- sweeper ------------------------------------------------------------------------------------------------------

func TestSweeperActsOnlyOnOverdueSagasAndOnlyOnce(t *testing.T) {
	r := newRig()
	id := r.place(t, "key-1", "")
	n, err := r.uc.SweepSagaTimeouts.Execute(context.Background(), 10)
	require.NoError(t, err)
	assert.Zero(t, n, "not overdue yet")

	r.clk.Advance(31 * time.Second)
	n, err = r.uc.SweepSagaTimeouts.Execute(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, saga.CmdReleaseStock, r.w.Types()[1], "step 1 timed out")
	o := r.order(t, id)
	assert.Equal(t, domain.StatusCancelling, o.Status)
	assert.Equal(t, saga.ReasonStepTimeout, o.CancelReason)
	assert.Equal(t, []domain.SagaState{domain.StateReservingStock}, r.m.timeouts)

	n, err = r.uc.SweepSagaTimeouts.Execute(context.Background(), 10)
	require.NoError(t, err)
	assert.Zero(t, n, "a second sweep, as another replica would run, finds nothing to do: handled once")
	assert.Len(t, r.w.Types(), 2)
}

func TestSweeperResendsCompensationsUntilAcknowledged(t *testing.T) {
	r := newRig()
	id := r.place(t, "key-1", "")
	r.clk.Advance(31 * time.Second)
	_, _ = r.uc.SweepSagaTimeouts.Execute(context.Background(), 10)
	for i := 0; i < 3; i++ {
		r.clk.Advance(21 * time.Second)
		n, err := r.uc.SweepSagaTimeouts.Execute(context.Background(), 10)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	}
	assert.Equal(t, []string{saga.CmdReserveStock, saga.CmdReleaseStock, saga.CmdReleaseStock, saga.CmdReleaseStock, saga.CmdReleaseStock}, r.w.Types())
	sg, _ := r.w.Saga(id)
	assert.Equal(t, 4, sg.Attempts, "the count that drives the alert")
	assert.Len(t, r.m.compensations, 4)

	r.reply(t, saga.StockReleased{OrderID: id})
	assert.Equal(t, domain.StatusCancelled, r.order(t, id).Status)
	r.clk.Advance(time.Hour)
	n, _ := r.uc.SweepSagaTimeouts.Execute(context.Background(), 10)
	assert.Zero(t, n, "a finished saga is never swept")
}

func TestSweeperNeverCancelsAfterThePayment(t *testing.T) {
	r := newRig()
	id := r.place(t, "key-1", "")
	r.reply(t, saga.StockReserved{OrderID: id})
	r.reply(t, saga.PaymentCompleted{OrderID: id, PaymentID: "p"})
	r.clk.Advance(31 * time.Second)
	n, err := r.uc.SweepSagaTimeouts.Execute(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, saga.CmdCommitStock, r.w.Types()[len(r.w.Types())-1], "CommitStock again")
	assert.Equal(t, domain.StatusPaymentProcessing, r.order(t, id).Status, "the order is not cancelled: the buyer was charged")
}

func TestSweeperHonoursItsLimit(t *testing.T) {
	r := newRig()
	for i := 0; i < 3; i++ {
		r.place(t, "key-"+string(rune('a'+i)), "")
	}
	r.clk.Advance(31 * time.Second)
	n, err := r.uc.SweepSagaTimeouts.Execute(context.Background(), 2)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	n, _ = r.uc.SweepSagaTimeouts.Execute(context.Background(), 2)
	assert.Equal(t, 1, n)
}

func TestSweeperStopsAtTheFirstError(t *testing.T) {
	r := newRig()
	r.place(t, "key-1", "")
	r.clk.Advance(31 * time.Second)
	r.w.FailOutbox = errors.New("outbox down")
	n, err := r.uc.SweepSagaTimeouts.Execute(context.Background(), 5)
	require.Error(t, err)
	assert.Zero(t, n)
	r.w.FailOutbox = nil
	n, err = r.uc.SweepSagaTimeouts.Execute(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the timeout was not lost with the failed attempt")
}

// ---- queries ------------------------------------------------------------------------------------------------------

func TestGetOrderShowsOnlyTheOwnersOrder(t *testing.T) {
	r := newRig()
	id := r.place(t, "key-1", "SALE10")
	v, err := r.uc.GetOrder.Execute(buyer("alice"), id)
	require.NoError(t, err)
	assert.Equal(t, id, v.ID)
	assert.Equal(t, domain.StatusPending, v.Status)
	assert.Equal(t, "card", v.PaymentMethod)
	assert.Len(t, v.Items, 2)

	_, err = r.uc.GetOrder.Execute(buyer("bob"), id)
	assert.Equal(t, domain.CodeOrderNotFound, errs.CodeOf(err), "someone else's order looks like a missing one")
	_, err = r.uc.GetOrder.Execute(buyer("alice"), "ghost")
	assert.Equal(t, domain.CodeOrderNotFound, errs.CodeOf(err), "an id that cannot exist is just not found")
	_, err = r.uc.GetOrder.Execute(buyer("alice"), "00000000-0000-4000-8000-00000000dead")
	assert.Equal(t, domain.CodeOrderNotFound, errs.CodeOf(err))
}

func TestListOrders(t *testing.T) {
	r := newRig()
	for i := 0; i < 3; i++ {
		r.place(t, "key-"+string(rune('a'+i)), "")
		r.clk.Advance(time.Second)
	}
	_, err := r.uc.PlaceOrder.Execute(buyer("bob"), placeIn("bob-key", ""))
	require.NoError(t, err)

	res, err := r.uc.ListOrders.Execute(buyer("alice"), 1, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 3, res.Total)
	require.Len(t, res.Items, 2)
	assert.True(t, res.Items[0].CreatedAt.After(res.Items[1].CreatedAt), "newest first")
	assert.Equal(t, 1, res.Page)
}

// capture swaps the default logger for one that records its lines.
type capture struct{ lines []map[string]any }

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }
func (c *capture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *capture) WithGroup(string) slog.Handler            { return c }
func (c *capture) Handle(_ context.Context, r slog.Record) error {
	m := map[string]any{"msg": r.Message, "level": r.Level.String()}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
	c.lines = append(c.lines, m)
	return nil
}

func TestEverySagaStepIsLoggedWithTheOrderAndTheState(t *testing.T) {
	c := &capture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(prev) })

	r := newRig()
	id := r.place(t, "key-1", "")
	r.reply(t, saga.StockReserved{OrderID: id})
	r.reply(t, saga.PaymentFailed{OrderID: id, Reason: saga.ReasonCardDeclined})
	r.reply(t, saga.StockReleased{OrderID: id})

	var steps []map[string]any
	for _, l := range c.lines {
		if l["msg"] == "saga step" {
			steps = append(steps, l)
		}
	}
	require.Len(t, steps, 3, "StockReserved, PaymentFailed, StockReleased")
	for _, s := range steps {
		assert.Equal(t, id, s["order_id"])
	}
	assert.Equal(t, "PROCESSING_PAYMENT", steps[0]["saga_state"])
	assert.Equal(t, "INFO", steps[0]["level"])
	assert.Equal(t, "COMPENSATING", steps[1]["saga_state"])
	assert.Equal(t, "WARN", steps[1]["level"], "a compensation is worth a second look")
	assert.Equal(t, saga.ReasonCardDeclined, steps[1]["reason"])
	assert.Equal(t, "FAILED", steps[2]["saga_state"])
	assert.Equal(t, "CANCELLED", steps[2]["order_status"])
}
