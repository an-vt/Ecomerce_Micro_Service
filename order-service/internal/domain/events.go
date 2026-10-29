package domain

import "github.com/an-vt/ecom/platform-go/kafkax"

// Topics the order events go to.
const (
	TopicConfirmed = kafkax.TopicOrderConfirmed
	TopicCancelled = kafkax.TopicOrderCancelled
)

// ConfirmedItem is a bought line in OrderConfirmed.
type ConfirmedItem struct {
	ProductID string `json:"productId"`
	Quantity  int64  `json:"quantity"`
}

// OrderConfirmed is published when the checkout completed: cart-service empties the bought units, notification-service
// sends ORDER-001 (docs/06 §6).
type OrderConfirmed struct {
	OrderID       string          `json:"orderId"`
	UserID        string          `json:"userId"`
	Items         []ConfirmedItem `json:"items"`
	TotalCheckout int64           `json:"totalCheckout"`
}

func (e OrderConfirmed) EventType() string   { return "order.order.confirmed" }
func (e OrderConfirmed) AggregateID() string { return e.OrderID }

// OrderCancelled is published when the checkout failed and everything was undone: notification-service sends ORDER-002.
type OrderCancelled struct {
	OrderID string `json:"orderId"`
	UserID  string `json:"userId"`
	Reason  string `json:"reason"`
}

func (e OrderCancelled) EventType() string   { return "order.order.cancelled" }
func (e OrderCancelled) AggregateID() string { return e.OrderID }
