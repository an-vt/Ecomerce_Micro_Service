// Package cache holds the Redis-backed SKU cache (monolith SkuService.oneSku: 30 s TTL, misses cached too).
package cache

import (
	"context"
	"time"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/money"
	"github.com/an-vt/ecom/platform-go/redisx"
)

type skuJSON struct {
	ID        string `json:"id"`
	SpuID     string `json:"spuId"`
	TierIdx   []int  `json:"tierIdx"`
	IsDefault bool   `json:"isDefault"`
	Slug      string `json:"slug"`
	Sort      int    `json:"sort"`
	Price     int64  `json:"price"`
	Stock     int64  `json:"stock"`
}

// Sku implements usecase.SkuCache on top of redisx.GetOrLoad.
type Sku struct {
	c   *redisx.Cache
	ttl time.Duration
}

// NewSku creates the cache; keys are prefixed "sku:". The same TTL applies to hits and to cached misses.
func NewSku(c *redisx.Cache, ttl time.Duration) *Sku { return &Sku{c: c, ttl: ttl} }

// Lookup returns the SKU from Redis, or loads and caches it. A Redis outage falls back to the loader.
func (s *Sku) Lookup(ctx context.Context, skuID string, load func(ctx context.Context) (*domain.Sku, bool, error)) (*domain.Sku, bool, error) {
	v, found, _, err := redisx.GetOrLoad(ctx, s.c, skuID, s.ttl, s.ttl, func(ctx context.Context) (skuJSON, bool, error) {
		sku, ok, err := load(ctx)
		if err != nil || !ok {
			return skuJSON{}, false, err
		}
		return skuJSON{ID: sku.ID, SpuID: sku.SpuID, TierIdx: sku.TierIdx, IsDefault: sku.IsDefault, Slug: sku.Slug, Sort: sku.Sort,
			Price: int64(sku.Price), Stock: sku.Stock}, true, nil
	})
	if err != nil || !found {
		return nil, false, err
	}
	return &domain.Sku{ID: v.ID, SpuID: v.SpuID, TierIdx: v.TierIdx, IsDefault: v.IsDefault, Slug: v.Slug, Sort: v.Sort,
		Price: money.Amount(v.Price), Stock: v.Stock}, true, nil
}
