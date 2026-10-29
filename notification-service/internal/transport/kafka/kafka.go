// Package kafka holds the message consumer of notification-service: it turns domain events into emails and in-app
// notifications (docs/08-messaging.md §1).
package kafka

import (
	"context"
	"time"

	"github.com/an-vt/ecom/notification-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/kafkax"
)

// Consumer is the consumer-group name.
const Consumer = "notification-service"

// Topics the service consumes.
var Topics = []string{kafkax.TopicUserRegistered, kafkax.TopicEmailVerification, kafkax.TopicProductCreated, kafkax.TopicDiscountCreated,
	kafkax.TopicOrderConfirmed, kafkax.TopicOrderCancelled}

type verificationRequested struct {
	Email     string    `json:"email"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type userRegistered struct {
	Email string `json:"email"`
}

type productCreated struct {
	ProductID string `json:"productId"`
	ShopID    string `json:"shopId"`
	Name      string `json:"name"`
	Price     int64  `json:"price"`
}

type discountCreated struct {
	DiscountID string `json:"discountId"`
	ShopID     string `json:"shopId"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Value      int64  `json:"value"`
}

type orderConfirmed struct {
	OrderID       string           `json:"orderId"`
	UserID        string           `json:"userId"`
	Items         []map[string]any `json:"items"`
	TotalCheckout int64            `json:"totalCheckout"`
}

type orderCancelled struct {
	OrderID string `json:"orderId"`
	UserID  string `json:"userId"`
	Reason  string `json:"reason"`
}

// Handler dispatches by message type. Nothing here needs an inbox: every use case is idempotent by itself (an email
// record and a notification are keyed by the message that caused them), so a redelivery does nothing twice.
func Handler(uc *usecase.Set) kafkax.Handler {
	return kafkax.NewMux().
		Handle("auth.email-verification.requested", func(ctx context.Context, env event.Envelope) error {
			var p verificationRequested
			if err := env.Decode(&p); err != nil {
				return err
			}
			return uc.SendVerificationEmail.Execute(ctx, env.MessageID, usecase.VerificationRequested{Email: p.Email, Link: p.Link, ExpiresAt: p.ExpiresAt})
		}).
		Handle("auth.user.registered", func(ctx context.Context, env event.Envelope) error {
			var p userRegistered
			if err := env.Decode(&p); err != nil {
				return err
			}
			return uc.SendWelcomeEmail.Execute(ctx, env.MessageID, usecase.UserRegistered{Email: p.Email})
		}).
		Handle("catalog.product.created", func(ctx context.Context, env event.Envelope) error {
			var p productCreated
			if err := env.Decode(&p); err != nil {
				return err
			}
			return uc.NotifyProductCreated.Execute(ctx, env.MessageID, usecase.ProductCreated{ProductID: p.ProductID, ShopID: p.ShopID, Name: p.Name, Price: p.Price})
		}).
		Handle("discount.discount.created", func(ctx context.Context, env event.Envelope) error {
			var p discountCreated
			if err := env.Decode(&p); err != nil {
				return err
			}
			return uc.NotifyDiscountCreated.Execute(ctx, env.MessageID, usecase.DiscountCreated{DiscountID: p.DiscountID, ShopID: p.ShopID, Code: p.Code, Name: p.Name, Type: p.Type, Value: p.Value})
		}).
		Handle("order.order.confirmed", func(ctx context.Context, env event.Envelope) error {
			var p orderConfirmed
			if err := env.Decode(&p); err != nil {
				return err
			}
			return uc.NotifyOrderConfirmed.Execute(ctx, env.MessageID, usecase.OrderConfirmed{OrderID: p.OrderID, UserID: p.UserID, TotalCheckout: p.TotalCheckout, Items: len(p.Items)})
		}).
		Handle("order.order.cancelled", func(ctx context.Context, env event.Envelope) error {
			var p orderCancelled
			if err := env.Decode(&p); err != nil {
				return err
			}
			return uc.NotifyOrderCancelled.Execute(ctx, env.MessageID, usecase.OrderCancelled{OrderID: p.OrderID, UserID: p.UserID, Reason: p.Reason})
		}).Handler()
}
