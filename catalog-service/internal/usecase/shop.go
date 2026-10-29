package usecase

import (
	"context"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// UpsertShop keeps the local shop projection current from auth.shop.registered.
type UpsertShop struct{ d *Deps }

// Execute stores the shop; replaying the same event is harmless.
func (u UpsertShop) Execute(ctx context.Context, s domain.ShopProjection) error {
	if s.ID == "" {
		return errs.Validation("shop id is required")
	}
	return u.d.Shops.Upsert(ctx, s)
}
