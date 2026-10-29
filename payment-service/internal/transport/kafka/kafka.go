// Package kafka holds the message consumer of payment-service: the saga commands of order-service
// (docs/08-messaging.md §1). payment-service has no synchronous API.
package kafka

import (
	"context"

	"github.com/an-vt/ecom/payment-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/saga"
)

// Consumer is the consumer-group name and the inbox owner.
const Consumer = "payment-service"

// Topics the service consumes.
var Topics = []string{saga.TopicPaymentCommands}

// Handler dispatches saga commands by message type. The use cases are idempotent themselves (one payment per order,
// the order id as the provider's idempotency key); the inbox adds an exactly-once guard on top, committed in the same
// transaction as the payment and its reply.
func Handler(uc *usecase.Set, tx inbox.TxManager, g inbox.Guard) kafkax.Handler {
	once := func(env event.Envelope, fn func(ctx context.Context) error) func(ctx context.Context) error {
		return func(ctx context.Context) error { return inbox.Handle(ctx, tx, g, Consumer, env.MessageID, fn) }
	}
	return kafkax.NewMux().
		Handle(saga.CmdProcessPayment, func(ctx context.Context, env event.Envelope) error {
			var c saga.ProcessPayment
			if err := env.Decode(&c); err != nil {
				return err
			}
			return once(env, func(ctx context.Context) error { return uc.ProcessPayment.Execute(ctx, c) })(ctx)
		}).
		Handle(saga.CmdRefundPayment, func(ctx context.Context, env event.Envelope) error {
			var c saga.RefundPayment
			if err := env.Decode(&c); err != nil {
				return err
			}
			return once(env, func(ctx context.Context) error { return uc.RefundPayment.Execute(ctx, c) })(ctx)
		}).Handler()
}
