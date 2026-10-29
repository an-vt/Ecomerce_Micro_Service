// Package postgres implements the order repositories with bun (docs/04-data-model.md §6). Every method takes its
// connection from pgdb.Store.DB(ctx), so it joins the surrounding transaction.
package postgres

import (
	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

// Repositories bundles the PostgreSQL implementations.
type Repositories struct {
	Orders domain.OrderRepository
	Sagas  domain.SagaRepository
	Keys   domain.IdempotencyRepository
}

// New creates the repositories over one store.
func New(s *pgdb.Store) Repositories {
	return Repositories{Orders: &orderRepo{s: s}, Sagas: &sagaRepo{s: s}, Keys: &keyRepo{s: s}}
}
