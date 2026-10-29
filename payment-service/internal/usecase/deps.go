package usecase

import (
	"context"

	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/saga"
)

// Deps is everything the use cases need.
type Deps struct {
	Payments domain.PaymentRepository
	Provider domain.Provider // already guarded: deadline + circuit breaker
	Tx       TxManager
	Outbox   Outbox
	Clock    clock.Clock
	IDs      idgen.Generator
	Currency string // ISO 4217, e.g. VND
}

func (d *Deps) currency() string {
	if d.Currency == "" {
		return "VND"
	}
	return d.Currency
}

// reply stores a saga reply in the outbox of the current transaction.
func (d *Deps) reply(ctx context.Context, m saga.Message) error {
	env, err := saga.Envelope(d.IDs.New(), d.Clock.Now(), m)
	if err != nil {
		return err
	}
	msg, err := outbox.New(ctx, saga.TopicReplies, env)
	if err != nil {
		return err
	}
	return d.Outbox.Add(ctx, msg)
}
