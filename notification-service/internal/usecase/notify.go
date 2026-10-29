package usecase

import (
	"context"
	"strconv"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Contents of the notifications. They go through the same renderer as emails, so every placeholder is filled.
const (
	contentNewProduct = "A shop just added a new product: {{name}}"
	contentPromotion  = "A shop just created a new voucher: {{name}} (code {{code}})"
	contentConfirmed  = "Your order {{orderId}} was confirmed. Total: {{total}}"
	contentCancelled  = "Your order {{orderId}} was cancelled: {{reason}}"
)

// notify stores one notification. The message id is its dedupe key: the same message delivered again creates nothing.
func (d *Deps) notify(ctx context.Context, typ domain.Type, sender, receiver, content string, params map[string]string, options map[string]any, messageID string) error {
	if messageID == "" { // without it every event would share one dedupe key and only the first would ever be stored
		return errs.Validation("a notification needs the id of the message that caused it")
	}
	text, err := domain.Render(content, params, false)
	if err != nil {
		return err
	}
	n, err := domain.NewNotification(d.IDs.New(), typ, sender, receiver, text, options, messageID+":"+string(typ)+":"+receiver, d.Clock.Now())
	if err != nil {
		return err
	}
	_, err = d.Notifications.Insert(ctx, n)
	return err
}

// ProductCreated is catalog.product.created.
type ProductCreated struct {
	ProductID, ShopID, Name string
	Price                   int64
}

// NotifyProductCreated tells everybody about a new product (SHOP-001).
type NotifyProductCreated struct{ d *Deps }

// Execute stores a broadcast from the shop.
func (u NotifyProductCreated) Execute(ctx context.Context, messageID string, ev ProductCreated) error {
	return u.d.notify(ctx, domain.TypeNewProduct, ev.ShopID, domain.Broadcast, contentNewProduct, map[string]string{"name": ev.Name},
		map[string]any{"productId": ev.ProductID, "shopId": ev.ShopID, "name": ev.Name, "price": ev.Price}, messageID)
}

// DiscountCreated is discount.discount.created.
type DiscountCreated struct {
	DiscountID, ShopID, Code, Name, Type string
	Value                                int64
}

// NotifyDiscountCreated tells everybody about a new voucher (PROMOTION-001).
type NotifyDiscountCreated struct{ d *Deps }

// Execute stores a broadcast from the shop.
func (u NotifyDiscountCreated) Execute(ctx context.Context, messageID string, ev DiscountCreated) error {
	return u.d.notify(ctx, domain.TypePromotion, ev.ShopID, domain.Broadcast, contentPromotion, map[string]string{"name": ev.Name, "code": ev.Code},
		map[string]any{"discountId": ev.DiscountID, "shopId": ev.ShopID, "code": ev.Code, "type": ev.Type, "value": ev.Value}, messageID)
}

// OrderConfirmed is order.order.confirmed.
type OrderConfirmed struct {
	OrderID, UserID string
	TotalCheckout   int64
	Items           int
}

// NotifyOrderConfirmed tells the buyer their order went through (ORDER-001).
type NotifyOrderConfirmed struct{ d *Deps }

// Execute stores a notification for the buyer.
func (u NotifyOrderConfirmed) Execute(ctx context.Context, messageID string, ev OrderConfirmed) error {
	return u.d.notify(ctx, domain.TypeOrderConfirmed, "order-service", ev.UserID, contentConfirmed,
		map[string]string{"orderId": ev.OrderID, "total": strconv.FormatInt(ev.TotalCheckout, 10)},
		map[string]any{"orderId": ev.OrderID, "total": ev.TotalCheckout, "items": ev.Items}, messageID)
}

// OrderCancelled is order.order.cancelled.
type OrderCancelled struct{ OrderID, UserID, Reason string }

// NotifyOrderCancelled tells the buyer their order did not go through, and why (ORDER-002).
type NotifyOrderCancelled struct{ d *Deps }

// Execute stores a notification for the buyer.
func (u NotifyOrderCancelled) Execute(ctx context.Context, messageID string, ev OrderCancelled) error {
	return u.d.notify(ctx, domain.TypeOrderCancelled, "order-service", ev.UserID, contentCancelled,
		map[string]string{"orderId": ev.OrderID, "reason": humanReason(ev.Reason)},
		map[string]any{"orderId": ev.OrderID, "reason": ev.Reason}, messageID)
}

// humanReason turns a saga failure reason into something a buyer can read.
func humanReason(code string) string {
	switch code {
	case "CARD_DECLINED":
		return "your card was declined"
	case "PROVIDER_UNAVAILABLE":
		return "the payment provider could not be reached"
	case "INSUFFICIENT_STOCK", "UNKNOWN_PRODUCT":
		return "an item is no longer available"
	case "STEP_TIMEOUT":
		return "the order took too long to process"
	case "":
		return "the order could not be completed"
	}
	if len(code) > 9 && code[:9] == "DISCOUNT_" {
		return "the discount code could not be used"
	}
	return "the order could not be completed"
}
