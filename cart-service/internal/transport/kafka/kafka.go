// Package kafka holds the message consumers of cart-service.
package kafka

import (
	"context"

	"github.com/an-vt/ecom/cart-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
)

// Consumer is the consumer-group name and the inbox owner.
const Consumer = "cart-service"

// Topics the service consumes.
var Topics = []string{kafkax.TopicOrderConfirmed}

type orderConfirmed struct {
	OrderID string `json:"orderId"`
	UserID  string `json:"userId"`
	Items   []struct {
		ProductID string `json:"productId"`
		Quantity  int64  `json:"quantity"`
	} `json:"items"`
}

// Handler empties bought lines from the buyer's cart when an order is confirmed (F-23). ClearOrdered removes units
// and so is not idempotent; the inbox makes each event take effect exactly once.
func Handler(uc *usecase.Set, tx inbox.TxManager, g inbox.Guard) kafkax.Handler {
	return kafkax.NewMux().
		Handle("order.order.confirmed", func(ctx context.Context, env event.Envelope) error {
			var p orderConfirmed
			if err := env.Decode(&p); err != nil {
				return err
			}
			lines := make([]usecase.OrderedLine, len(p.Items))
			for i, it := range p.Items {
				lines[i] = usecase.OrderedLine{ProductID: it.ProductID, Quantity: it.Quantity}
			}
			return inbox.Handle(ctx, tx, g, Consumer, env.MessageID, func(ctx context.Context) error {
				return uc.ClearOrdered.Execute(ctx, p.UserID, lines)
			})
		}).Handler()
}
