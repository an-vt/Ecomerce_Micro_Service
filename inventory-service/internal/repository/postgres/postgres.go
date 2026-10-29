// Package postgres implements the inventory repositories with bun (docs/04-data-model.md §3). Every method takes
// its connection from pgdb.Store.DB(ctx), so it joins the surrounding transaction.
package postgres

import (
	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

// Repositories bundles the PostgreSQL implementations.
type Repositories struct {
	Inventories  domain.InventoryRepository
	Reservations domain.ReservationRepository
}

// New creates both repositories over one store.
func New(s *pgdb.Store) Repositories {
	return Repositories{Inventories: &inventoryRepo{s: s}, Reservations: &reservationRepo{s: s}}
}
