package usecase

import (
	"context"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/saga"
)

// Deps is everything the use cases need.
type Deps struct {
	Discounts domain.DiscountRepository
	Usages    domain.UsageRepository
	Catalog   domain.Catalog
	Tx        TxManager
	Outbox    Outbox
	Clock     clock.Clock
	IDs       idgen.Generator
}

func (d *Deps) add(ctx context.Context, topic string, env event.Envelope) error {
	msg, err := outbox.New(ctx, topic, env)
	if err != nil {
		return err
	}
	return d.Outbox.Add(ctx, msg)
}

// emit stores a domain event in the outbox of the current transaction.
func (d *Deps) emit(ctx context.Context, topic string, ev event.Event) error {
	env, err := event.FromEvent(d.IDs.New(), d.Clock.Now(), ev)
	if err != nil {
		return err
	}
	return d.add(ctx, topic, env)
}

// reply stores a saga reply in the outbox of the current transaction.
func (d *Deps) reply(ctx context.Context, m saga.Message) error {
	env, err := saga.Envelope(d.IDs.New(), d.Clock.Now(), m)
	if err != nil {
		return err
	}
	return d.add(ctx, saga.TopicReplies, env)
}

func caller(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.From(ctx)
	if !ok {
		return principal.Principal{}, errs.Unauthenticated("UNAUTHENTICATED", "this call needs an authenticated caller")
	}
	return p, nil
}

func shopCaller(ctx context.Context) (principal.Principal, error) {
	p, err := caller(ctx)
	if err != nil {
		return p, err
	}
	if !p.IsShop() {
		return p, errs.PermissionDenied("FORBIDDEN", "only shops can manage discounts")
	}
	return p, nil
}

func userCaller(ctx context.Context) (principal.Principal, error) {
	p, err := caller(ctx)
	if err != nil {
		return p, err
	}
	if !p.IsUser() {
		return p, errs.PermissionDenied("FORBIDDEN", "only buyers can use discount codes")
	}
	return p, nil
}

// ownProducts checks that every id is a product of the shop: a code must not discount another shop's products.
func (d *Deps) ownProducts(ctx context.Context, shopID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	own, err := d.Catalog.ProductIDsByShop(ctx, shopID)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(own))
	for _, id := range own {
		have[id] = true
	}
	for _, id := range ids {
		if !have[id] {
			return errs.InvalidArgument(domain.CodeUnknownProducts, "product "+id+" is not one of this shop's products")
		}
	}
	return nil
}
