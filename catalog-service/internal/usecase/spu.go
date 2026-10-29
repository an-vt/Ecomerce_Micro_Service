package usecase

import (
	"context"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// CreateSpu adds an SPU with its SKUs for the calling shop (monolith SpuService.newSpu + SkuService.newSku).
type CreateSpu struct{ d *Deps }

// Execute validates the variation structure and stores the SPU and every SKU atomically.
func (u CreateSpu) Execute(ctx context.Context, in domain.SpuInput) (*domain.Spu, []*domain.Sku, error) {
	shop, err := shopCaller(ctx)
	if err != nil {
		return nil, nil, err
	}
	spu, skus, err := domain.NewSpu(u.d.IDs.New(), shop.ID, in, u.d.Clock.Now())
	if err != nil {
		return nil, nil, err
	}
	err = u.d.Tx.WithinTx(ctx, func(ctx context.Context) error { return u.d.Spus.Create(ctx, spu, skus) })
	if err != nil {
		return nil, nil, err
	}
	return spu, skus, nil
}

// GetSpu returns an SPU with its SKUs.
type GetSpu struct{ d *Deps }

// Execute loads the SPU page.
func (u GetSpu) Execute(ctx context.Context, spuID string) (*domain.Spu, []*domain.Sku, error) {
	spu, err := u.d.Spus.FindByID(ctx, spuID)
	if err != nil {
		return nil, nil, err
	}
	skus, err := u.d.Spus.SkusOf(ctx, spuID)
	if err != nil {
		return nil, nil, err
	}
	return spu, skus, nil
}

// GetSku returns one SKU through the cache (monolith SkuService.oneSku with its 30 s TTL and null caching).
type GetSku struct{ d *Deps }

// Execute looks the SKU up; unknown ids are cached as misses so repeated probing does not reach MongoDB. A SKU of
// another SPU is reported as not found.
func (u GetSku) Execute(ctx context.Context, skuID, spuID string) (*domain.Sku, error) {
	if skuID == "" || spuID == "" {
		return nil, errs.Validation("sku_id and spu_id are required")
	}
	sku, found, err := u.d.SkuCache.Lookup(ctx, skuID, func(ctx context.Context) (*domain.Sku, bool, error) {
		s, err := u.d.Spus.FindSku(ctx, skuID, spuID)
		switch {
		case err == nil:
			return s, true, nil
		case errs.CodeOf(err) == domain.CodeSkuNotFound:
			return nil, false, nil
		}
		return nil, false, err
	})
	if err != nil {
		return nil, err
	}
	if !found || sku.SpuID != spuID {
		return nil, domain.ErrSkuNotFound()
	}
	return sku, nil
}
