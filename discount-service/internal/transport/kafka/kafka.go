// Package kafka holds the message consumer of discount-service: the saga commands of order-service
// (docs/08-messaging.md §1).
package kafka

import (
	"context"

	"github.com/an-vt/ecom/discount-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/saga"
)

// Consumer is the consumer-group name and the inbox owner.
const Consumer = "discount-service"

// Topics the service consumes.
var Topics = []string{saga.TopicDiscountCommands}

// Handler dispatches saga commands by message type. The use cases are idempotent themselves; the inbox adds an
// exactly-once guard on top, committed in the same transaction as the redemption and its reply.
func Handler(uc *usecase.Set, tx inbox.TxManager, g inbox.Guard) kafkax.Handler {
	once := func(env event.Envelope, fn func(ctx context.Context) error) func(ctx context.Context) error {
		return func(ctx context.Context) error { return inbox.Handle(ctx, tx, g, Consumer, env.MessageID, fn) }
	}
	return kafkax.NewMux().
		Handle(saga.CmdApplyDiscounts, func(ctx context.Context, env event.Envelope) error {
			var c saga.ApplyDiscounts
			if err := env.Decode(&c); err != nil {
				return err
			}
			return once(env, func(ctx context.Context) error { return uc.ApplyDiscounts.Execute(ctx, c) })(ctx)
		}).
		Handle(saga.CmdReleaseDiscount, func(ctx context.Context, env event.Envelope) error {
			var c saga.ReleaseDiscounts
			if err := env.Decode(&c); err != nil {
				return err
			}
			return once(env, func(ctx context.Context) error { return uc.ReleaseDiscounts.Execute(ctx, c) })(ctx)
		}).Handler()
}
