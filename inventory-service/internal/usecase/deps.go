package usecase

import (
	"context"
	"time"

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/saga"
)

// Deps is everything the use cases need.
type Deps struct {
	Inventories    domain.InventoryRepository
	Reservations   domain.ReservationRepository
	Tx             TxManager
	Outbox         Outbox
	Clock          clock.Clock
	IDs            idgen.Generator
	ReservationTTL time.Duration // how long an unfinished order may hold stock; default 15 minutes
}

func (d *Deps) ttl() time.Duration {
	if d.ReservationTTL <= 0 {
		return 15 * time.Minute
	}
	return d.ReservationTTL
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

func shopCaller(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.From(ctx)
	if !ok {
		return principal.Principal{}, errs.Unauthenticated("UNAUTHENTICATED", "this call needs an authenticated caller")
	}
	if !p.IsShop() {
		return principal.Principal{}, errs.PermissionDenied("FORBIDDEN", "only shops can manage stock")
	}
	return p, nil
}
