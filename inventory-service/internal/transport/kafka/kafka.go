// Package kafka holds the message consumers of inventory-service: catalog events that keep the stock records in sync,
// and the saga commands of order-service (docs/08-messaging.md §1).
package kafka

import (
	"context"

	"github.com/an-vt/ecom/inventory-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/saga"
)

// Consumer is the consumer-group name and the inbox owner.
const Consumer = "inventory-service"

// Topics the service consumes.
var Topics = []string{kafkax.TopicProductCreated, kafkax.TopicProductUpdated, saga.TopicInventoryCommands}

type productCreated struct {
	ProductID string `json:"productId"`
	ShopID    string `json:"shopId"`
	Quantity  int64  `json:"quantity"`
}

type productUpdated struct {
	ProductID string `json:"productId"`
	ShopID    string `json:"shopId"`
	Quantity  *int64 `json:"quantity"`
}

// once runs fn exactly once per message: the inbox record and the effects commit together.
func once(tx inbox.TxManager, g inbox.Guard, env event.Envelope, fn func(ctx context.Context) error) func(ctx context.Context) error {
	return func(ctx context.Context) error { return inbox.Handle(ctx, tx, g, Consumer, env.MessageID, fn) }
}

// Handler dispatches by message type. Saga commands are handled idempotently by the use cases themselves, and the
// inbox adds an exactly-once guard on top.
func Handler(uc *usecase.Set, tx inbox.TxManager, g inbox.Guard) kafkax.Handler {
	return kafkax.NewMux().
		Handle("catalog.product.created", func(ctx context.Context, env event.Envelope) error {
			var p productCreated
			if err := env.Decode(&p); err != nil {
				return err
			}
			return once(tx, g, env, func(ctx context.Context) error {
				return uc.CreateInventory.Execute(ctx, usecase.ProductCreated{ProductID: p.ProductID, ShopID: p.ShopID, Quantity: p.Quantity})
			})(ctx)
		}).
		Handle("catalog.product.updated", func(ctx context.Context, env event.Envelope) error {
			var p productUpdated
			if err := env.Decode(&p); err != nil {
				return err
			}
			return once(tx, g, env, func(ctx context.Context) error {
				return uc.SyncStock.Execute(ctx, usecase.ProductUpdated{ProductID: p.ProductID, ShopID: p.ShopID, Quantity: p.Quantity})
			})(ctx)
		}).
		Handle(saga.CmdReserveStock, func(ctx context.Context, env event.Envelope) error {
			var c saga.ReserveStock
			if err := env.Decode(&c); err != nil {
				return err
			}
			return once(tx, g, env, func(ctx context.Context) error { return uc.ReserveStock.Execute(ctx, c) })(ctx)
		}).
		Handle(saga.CmdReleaseStock, func(ctx context.Context, env event.Envelope) error {
			var c saga.ReleaseStock
			if err := env.Decode(&c); err != nil {
				return err
			}
			return once(tx, g, env, func(ctx context.Context) error { return uc.ReleaseStock.Execute(ctx, c) })(ctx)
		}).
		Handle(saga.CmdCommitStock, func(ctx context.Context, env event.Envelope) error {
			var c saga.CommitStock
			if err := env.Decode(&c); err != nil {
				return err
			}
			return once(tx, g, env, func(ctx context.Context) error { return uc.CommitStock.Execute(ctx, c) })(ctx)
		}).Handler()
}
