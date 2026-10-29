// Package kafka holds the message consumer of order-service: the replies of the saga participants
// (docs/08-messaging.md §1).
package kafka

import (
	"context"

	"github.com/an-vt/ecom/order-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/saga"
)

// Consumer is the consumer-group name and the inbox owner.
const Consumer = "order-service"

// Topics the service consumes.
var Topics = []string{saga.TopicReplies}

// Handler routes each reply type to HandleSagaReply. The inbox makes every message take effect once; a duplicate with
// a new message id (a participant answering a redelivered command again) is handled by the saga itself, which ignores
// replies that no longer fit.
func Handler(uc *usecase.Set, tx inbox.TxManager, g inbox.Guard) kafkax.Handler {
	mux := kafkax.NewMux()
	route[saga.StockReserved](mux, saga.ReplyStockReserved, uc, tx, g)
	route[saga.StockReservationFailed](mux, saga.ReplyStockReservationFailed, uc, tx, g)
	route[saga.StockReleased](mux, saga.ReplyStockReleased, uc, tx, g)
	route[saga.StockCommitted](mux, saga.ReplyStockCommitted, uc, tx, g)
	route[saga.DiscountsApplied](mux, saga.ReplyDiscountsApplied, uc, tx, g)
	route[saga.DiscountsRejected](mux, saga.ReplyDiscountsRejected, uc, tx, g)
	route[saga.DiscountsReleased](mux, saga.ReplyDiscountsReleased, uc, tx, g)
	route[saga.PaymentCompleted](mux, saga.ReplyPaymentCompleted, uc, tx, g)
	route[saga.PaymentFailed](mux, saga.ReplyPaymentFailed, uc, tx, g)
	route[saga.PaymentRefunded](mux, saga.ReplyPaymentRefunded, uc, tx, g)
	return mux.Handler()
}

func route[T saga.Message](mux *kafkax.Mux, typ string, uc *usecase.Set, tx inbox.TxManager, g inbox.Guard) {
	mux.Handle(typ, func(ctx context.Context, env event.Envelope) error {
		var m T
		if err := env.Decode(&m); err != nil {
			return err
		}
		return inbox.Handle(ctx, tx, g, Consumer, env.MessageID, func(ctx context.Context) error { return uc.HandleSagaReply.Execute(ctx, m) })
	})
}
