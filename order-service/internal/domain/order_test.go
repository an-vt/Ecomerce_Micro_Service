package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func input() domain.NewOrderInput {
	return domain.NewOrderInput{ID: "o1", UserID: "u1", FeeShip: 5000,
		Items: []domain.Item{
			{ProductID: "p1", ShopID: "s1", Name: "Shirt", Price: 100000, Quantity: 2},
			{ProductID: "p2", ShopID: "s2", Name: "Hat", Price: 50000, Quantity: 1}},
		Discounts:     []domain.DiscountRef{{ShopID: "s1", Code: "SALE10"}},
		Shipping:      domain.Shipping{Name: "An", Address: "1 Main St", City: "HCMC"},
		PaymentMethod: " card ", PaymentToken: "tok_ok"}
}

func newOrder(t *testing.T) *domain.Order {
	t.Helper()
	o, err := domain.NewOrder(input(), now)
	require.NoError(t, err)
	return o
}

func TestNewOrderComputesTheTotalFromItsItems(t *testing.T) {
	o := newOrder(t)
	s := o.Snapshot()
	assert.Equal(t, domain.StatusPending, s.Status)
	assert.EqualValues(t, 250000, s.TotalPrice)
	assert.EqualValues(t, 5000, s.FeeShip)
	assert.EqualValues(t, 255000, s.TotalCheckout, "items + shipping, no discount yet")
	assert.EqualValues(t, 0, s.TotalDiscount)
	assert.Equal(t, "card", s.PaymentMethod)
	assert.Equal(t, now, s.CreatedAt)
	assert.True(t, o.HasDiscounts())
	assert.EqualValues(t, 255000, o.AmountDue())
	assert.Equal(t, "o1", o.ID())
	assert.Equal(t, "u1", o.UserID())
	assert.Equal(t, s, domain.RestoreOrder(s).Snapshot())
}

func TestNewOrderRefusals(t *testing.T) {
	cases := map[string]func(*domain.NewOrderInput){
		"no id":          func(in *domain.NewOrderInput) { in.ID = "" },
		"no buyer":       func(in *domain.NewOrderInput) { in.UserID = "" },
		"no items":       func(in *domain.NewOrderInput) { in.Items = nil },
		"zero quantity":  func(in *domain.NewOrderInput) { in.Items[0].Quantity = 0 },
		"negative price": func(in *domain.NewOrderInput) { in.Items[0].Price = -1 },
		"no shop":        func(in *domain.NewOrderInput) { in.Items[0].ShopID = "" },
		"no product":     func(in *domain.NewOrderInput) { in.Items[0].ProductID = "" },
		"negative fee":   func(in *domain.NewOrderInput) { in.FeeShip = -1 },
		"no address":     func(in *domain.NewOrderInput) { in.Shipping.Address = "  " },
		"no method":      func(in *domain.NewOrderInput) { in.PaymentMethod = "" },
		"no token":       func(in *domain.NewOrderInput) { in.PaymentToken = "" },
		"line overflow":  func(in *domain.NewOrderInput) { in.Items[0].Price, in.Items[0].Quantity = 1<<62, 8 },
		"total overflow": func(in *domain.NewOrderInput) {
			in.Items[0].Price, in.Items[0].Quantity = 1<<62, 1
			in.Items[1].Price, in.Items[1].Quantity = 1<<62, 2
		},
		"fee overflow": func(in *domain.NewOrderInput) {
			in.Items[0].Price, in.Items[0].Quantity = 1<<62, 1
			in.Items[1].Price, in.Items[1].Quantity = 1<<62, 1
			in.FeeShip = 1 << 62
		},
	}
	for name, mut := range cases {
		in := input()
		in.Items = append([]domain.Item(nil), in.Items...)
		mut(&in)
		_, err := domain.NewOrder(in, now)
		require.Error(t, err, name)
		assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err), name)
	}
}

func TestTheStatusGuardAllowsOnlyTheDocumentedArrows(t *testing.T) {
	all := []domain.Status{domain.StatusPending, domain.StatusStockReserved, domain.StatusDiscountApplied, domain.StatusPaymentProcessing,
		domain.StatusConfirmed, domain.StatusCancelling, domain.StatusCancelled}
	ok := map[[2]domain.Status]bool{
		{domain.StatusPending, domain.StatusStockReserved}:             true,
		{domain.StatusPending, domain.StatusCancelling}:                true,
		{domain.StatusStockReserved, domain.StatusDiscountApplied}:     true,
		{domain.StatusStockReserved, domain.StatusPaymentProcessing}:   true,
		{domain.StatusStockReserved, domain.StatusCancelling}:          true,
		{domain.StatusDiscountApplied, domain.StatusPaymentProcessing}: true,
		{domain.StatusDiscountApplied, domain.StatusCancelling}:        true,
		{domain.StatusPaymentProcessing, domain.StatusConfirmed}:       true,
		{domain.StatusPaymentProcessing, domain.StatusCancelling}:      true,
		{domain.StatusCancelling, domain.StatusCancelled}:              true,
	}
	for _, from := range all {
		for _, to := range all {
			o := domain.RestoreOrder(domain.OrderSnapshot{ID: "o", Status: from})
			err := o.Advance(to, now)
			if ok[[2]domain.Status{from, to}] {
				require.NoError(t, err, "%s -> %s", from, to)
				assert.Equal(t, to, o.Status())
				assert.EqualValues(t, 1, o.Snapshot().Version, "every transition bumps the version")
			} else {
				require.Error(t, err, "%s -> %s", from, to)
				assert.Equal(t, domain.CodeInvalidState, errs.CodeOf(err))
				assert.Equal(t, from, o.Status(), "a refused transition changes nothing")
			}
		}
	}
	assert.True(t, domain.StatusConfirmed.Terminal())
	assert.True(t, domain.StatusCancelled.Terminal())
	assert.False(t, domain.StatusCancelling.Terminal())
}

func TestCancellingRemembersWhy(t *testing.T) {
	o := newOrder(t)
	require.NoError(t, o.StartCancelling("CARD_DECLINED", now.Add(time.Second)))
	assert.Equal(t, domain.StatusCancelling, o.Status())
	assert.Equal(t, "CARD_DECLINED", o.Snapshot().CancelReason)
	assert.Equal(t, now.Add(time.Second), o.Snapshot().UpdatedAt)
	require.NoError(t, o.Advance(domain.StatusCancelled, now))
	assert.Error(t, o.StartCancelling("again", now), "a cancelled order cannot be cancelled again")
	assert.Equal(t, "CARD_DECLINED", o.Snapshot().CancelReason, "and keeps its first reason")
}

func TestApplyDiscountSetsTheAmountToCharge(t *testing.T) {
	o := newOrder(t)
	assert.Error(t, o.ApplyDiscount(1000, now), "stock must be held first")
	require.NoError(t, o.Advance(domain.StatusStockReserved, now))
	require.NoError(t, o.ApplyDiscount(25000, now))
	s := o.Snapshot()
	assert.EqualValues(t, 25000, s.TotalDiscount)
	assert.EqualValues(t, 230000, s.TotalCheckout, "250000 - 25000 + 5000 shipping")
	assert.EqualValues(t, 230000, o.AmountDue())
	assert.EqualValues(t, 250000, s.TotalPrice, "the price of the items does not change")

	assert.Error(t, o.ApplyDiscount(-1, now))
	assert.Error(t, o.ApplyDiscount(250001, now), "a discount cannot exceed the items")
	require.NoError(t, o.ApplyDiscount(250000, now))
	assert.EqualValues(t, 5000, o.AmountDue(), "a fully discounted order still pays shipping")
}

func TestSnapshotIsACopy(t *testing.T) {
	o := newOrder(t)
	s := o.Snapshot()
	s.Items[0].Name = "hacked"
	s.Discounts[0].Code = "hacked"
	assert.Equal(t, "Shirt", o.Snapshot().Items[0].Name)
	assert.Equal(t, "SALE10", o.Snapshot().Discounts[0].Code)
}

func TestEventsAndErrorsHaveStableNames(t *testing.T) {
	c := domain.OrderConfirmed{OrderID: "o1"}
	assert.Equal(t, "order.order.confirmed", c.EventType())
	assert.Equal(t, "o1", c.AggregateID())
	x := domain.OrderCancelled{OrderID: "o2"}
	assert.Equal(t, "order.order.cancelled", x.EventType())
	assert.Equal(t, "o2", x.AggregateID())
	assert.Equal(t, "order.order.confirmed", domain.TopicConfirmed)
	assert.Equal(t, "order.order.cancelled", domain.TopicCancelled)
	assert.Equal(t, errs.KindNotFound, errs.KindOf(domain.ErrNotFound()))
	assert.Equal(t, errs.KindAborted, errs.KindOf(domain.ErrIdempotencyConflict()))
	assert.Equal(t, domain.CodeSagaNotFound, errs.CodeOf(domain.ErrSagaNotFound()))
}

func TestValidID(t *testing.T) {
	assert.True(t, domain.ValidID("00000000-0000-4000-8000-000000000001"))
	assert.True(t, domain.ValidID("6DC78670-7AF8-479C-9E24-A736FD8E7E8C"))
	for _, bad := range []string{"", "ghost", "e2e-order-1", "00000000-0000-4000-8000-00000000000", "00000000-0000-4000-8000-0000000000012", " 00000000-0000-4000-8000-000000000001",
		"0000000g-0000-4000-8000-000000000001"} {
		assert.False(t, domain.ValidID(bad), bad)
	}
}
