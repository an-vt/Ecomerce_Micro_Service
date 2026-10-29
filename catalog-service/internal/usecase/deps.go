package usecase

import (
	"context"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/principal"
)

// Deps is everything the use cases need.
type Deps struct {
	Products domain.ProductRepository
	Spus     domain.SpuRepository
	Comments domain.CommentRepository
	Shops    domain.ShopRepository

	Types    *domain.TypeRegistry
	Tx       TxManager
	Outbox   Outbox
	SkuCache SkuCache
	Clock    clock.Clock
	IDs      idgen.Generator
}

func (d *Deps) emit(ctx context.Context, topic string, ev event.Event) error {
	env, err := event.FromEvent(d.IDs.New(), d.Clock.Now(), ev)
	if err != nil {
		return err
	}
	msg, err := outbox.New(ctx, topic, env)
	if err != nil {
		return err
	}
	return d.Outbox.Add(ctx, msg)
}

// shopCaller returns the authenticated shop; the gateway already enforces it, this is defence in depth.
func shopCaller(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.From(ctx)
	if !ok {
		return principal.Principal{}, errs.Unauthenticated("UNAUTHENTICATED", "this call needs an authenticated caller")
	}
	if !p.IsShop() {
		return principal.Principal{}, errs.PermissionDenied(domain.CodeForbidden, "only shops can manage products")
	}
	return p, nil
}

func anyCaller(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.From(ctx)
	if !ok {
		return principal.Principal{}, errs.Unauthenticated("UNAUTHENTICATED", "this call needs an authenticated caller")
	}
	return p, nil
}
