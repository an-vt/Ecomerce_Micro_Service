// Package postgres implements the discount repositories with bun (docs/04-data-model.md §5). Every method takes its
// connection from pgdb.Store.DB(ctx), so it joins the surrounding transaction.
package postgres

import (
	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

// Repositories bundles the PostgreSQL implementations.
type Repositories struct {
	Discounts domain.DiscountRepository
	Usages    domain.UsageRepository
}

// New creates both repositories over one store.
func New(s *pgdb.Store) Repositories {
	return Repositories{Discounts: &discountRepo{s: s}, Usages: &usageRepo{s: s}}
}
