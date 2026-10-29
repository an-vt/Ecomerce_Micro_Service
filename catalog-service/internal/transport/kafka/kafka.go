// Package kafka holds the message consumers of catalog-service.
package kafka

import (
	"context"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/catalog-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
)

// Consumer name used for the inbox: one record per (consumer, message).
const Consumer = "catalog-service"

// Topics the service consumes.
var Topics = []string{kafkax.TopicShopRegistered}

type shopRegistered struct {
	ShopID string `json:"shopId"`
	Name   string `json:"name"`
	Email  string `json:"email"`
}

// Handler dispatches auth.shop.registered to the shop projection, once per message (inbox).
func Handler(uc *usecase.Set, tx inbox.TxManager, guard inbox.Guard) kafkax.Handler {
	return kafkax.NewMux().
		Handle("auth.shop.registered", func(ctx context.Context, env event.Envelope) error {
			var p shopRegistered
			if err := env.Decode(&p); err != nil {
				return err
			}
			return inbox.Handle(ctx, tx, guard, Consumer, env.MessageID, func(ctx context.Context) error {
				return uc.UpsertShop.Execute(ctx, domain.ShopProjection{ID: p.ShopID, Name: p.Name, Email: p.Email})
			})
		}).Handler()
}
