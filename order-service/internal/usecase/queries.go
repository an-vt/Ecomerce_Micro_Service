package usecase

import (
	"context"
	"time"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pagination"
)

// OrderView is an order as the transports show it. The payment token is deliberately not part of it.
type OrderView struct {
	ID             string
	UserID         string
	Status         domain.Status
	TotalPrice     int64
	TotalDiscount  int64
	FeeShip        int64
	TotalCheckout  int64
	Shipping       domain.Shipping
	PaymentMethod  string
	Items          []domain.Item
	Discounts      []domain.DiscountRef
	CancelReason   string
	TrackingNumber string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func viewOf(o *domain.Order) OrderView {
	s := o.Snapshot()
	return OrderView{ID: s.ID, UserID: s.UserID, Status: s.Status, TotalPrice: s.TotalPrice, TotalDiscount: s.TotalDiscount, FeeShip: s.FeeShip,
		TotalCheckout: s.TotalCheckout, Shipping: s.Shipping, PaymentMethod: s.PaymentMethod, Items: s.Items, Discounts: s.Discounts,
		CancelReason: s.CancelReason, TrackingNumber: s.TrackingNumber, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

// GetOrder returns one of the caller's orders.
type GetOrder struct{ d *Deps }

// Execute answers NOT_FOUND for an order that belongs to someone else, the same as for one that does not exist.
func (u GetOrder) Execute(ctx context.Context, id string) (OrderView, error) {
	me, err := userCaller(ctx)
	if err != nil {
		return OrderView{}, err
	}
	if !domain.ValidID(id) {
		return OrderView{}, domain.ErrNotFound()
	}
	o, err := u.d.Orders.Find(ctx, id)
	if err != nil {
		return OrderView{}, err
	}
	if o.UserID() != me.ID {
		return OrderView{}, domain.ErrNotFound()
	}
	return viewOf(o), nil
}

// ListOrders lists the caller's orders.
type ListOrders struct{ d *Deps }

// Execute returns one page, newest first.
func (u ListOrders) Execute(ctx context.Context, page, limit int) (pagination.Result[OrderView], error) {
	me, err := userCaller(ctx)
	if err != nil {
		return pagination.Result[OrderView]{}, err
	}
	pg := pagination.New(page, limit)
	orders, total, err := u.d.Orders.ListByUser(ctx, me.ID, pg.Offset(), pg.Limit)
	if err != nil {
		return pagination.Result[OrderView]{}, err
	}
	views := make([]OrderView, len(orders))
	for i, o := range orders {
		views[i] = viewOf(o)
	}
	return pagination.NewResult(pg, views, total), nil
}
