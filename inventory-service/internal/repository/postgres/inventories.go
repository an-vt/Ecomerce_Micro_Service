package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type inventoryRow struct {
	bun.BaseModel `bun:"table:inventories"`

	ID        string    `bun:"id,pk"`
	ProductID string    `bun:"product_id"`
	ShopID    string    `bun:"shop_id"`
	Location  string    `bun:"location"`
	Stock     int64     `bun:"stock"`
	Reserved  int64     `bun:"reserved"`
	CreatedAt time.Time `bun:"created_at"`
	UpdatedAt time.Time `bun:"updated_at"`
}

func (r *inventoryRow) toDomain() *domain.Inventory {
	return domain.RestoreInventory(domain.InventorySnapshot{ID: r.ID, ProductID: r.ProductID, ShopID: r.ShopID, Location: r.Location,
		Stock: r.Stock, Reserved: r.Reserved, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()})
}

type inventoryRepo struct{ s *pgdb.Store }

func (r *inventoryRepo) Insert(ctx context.Context, inv *domain.Inventory) (bool, error) {
	sn := inv.Snapshot()
	res, err := r.s.DB(ctx).NewInsert().Model(&inventoryRow{ID: sn.ID, ProductID: sn.ProductID, ShopID: sn.ShopID, Location: sn.Location,
		Stock: sn.Stock, Reserved: sn.Reserved, CreatedAt: sn.CreatedAt, UpdatedAt: sn.UpdatedAt}).
		On("CONFLICT (product_id) DO NOTHING").Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Lock takes row locks in product-id order. Two transactions that need overlapping products therefore queue up in the
// same order and cannot deadlock; the second one then sees the first one's committed numbers.
func (r *inventoryRepo) Lock(ctx context.Context, ids []string) (map[string]*domain.Inventory, error) {
	out := map[string]*domain.Inventory{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []inventoryRow
	if err := r.s.DB(ctx).NewSelect().Model(&rows).Where("product_id IN (?)", bun.List(ids)).OrderExpr("product_id").For("UPDATE").Scan(ctx); err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].ProductID] = rows[i].toDomain()
	}
	return out, nil
}

func (r *inventoryRepo) Find(ctx context.Context, productID string) (*domain.Inventory, error) {
	row := new(inventoryRow)
	err := r.s.DB(ctx).NewSelect().Model(row).Where("product_id = ?", productID).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrInventoryNotFound()
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}

func (r *inventoryRepo) Save(ctx context.Context, inv *domain.Inventory) error {
	sn := inv.Snapshot()
	res, err := r.s.DB(ctx).NewUpdate().Model((*inventoryRow)(nil)).
		Set("stock = ?", sn.Stock).Set("reserved = ?", sn.Reserved).Set("location = ?", sn.Location).Set("updated_at = ?", sn.UpdatedAt).
		Where("product_id = ?", sn.ProductID).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrInventoryNotFound()
	}
	return nil
}
