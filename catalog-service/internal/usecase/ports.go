// Package usecase holds the application logic of catalog-service. It depends on the domain and on the small
// interfaces below; concrete adapters (MongoDB, Redis, …) are wired in internal/app.
package usecase

import (
	"context"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// TxManager runs fn in one database transaction.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Outbox adds messages in the transaction of ctx.
type Outbox interface {
	Add(ctx context.Context, msgs ...outbox.Message) error
}

// SkuCache is the cache-aside lookup of SKUs. Lookup returns the cached SKU, or calls load and caches what it
// returns — a missing SKU too, which protects the database from repeated lookups of unknown ids.
type SkuCache interface {
	Lookup(ctx context.Context, skuID string, load func(ctx context.Context) (*domain.Sku, bool, error)) (sku *domain.Sku, found bool, err error)
}
