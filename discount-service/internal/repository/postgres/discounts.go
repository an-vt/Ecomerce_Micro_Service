package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type discountRow struct {
	bun.BaseModel `bun:"table:discounts"`

	ID             string    `bun:"id,pk"`
	ShopID         string    `bun:"shop_id"`
	Code           string    `bun:"code"`
	Name           string    `bun:"name"`
	Description    string    `bun:"description"`
	Type           string    `bun:"type"`
	Value          int64     `bun:"value"`
	StartDate      time.Time `bun:"start_date"`
	EndDate        time.Time `bun:"end_date"`
	MaxUses        int64     `bun:"max_uses"`
	UsedCount      int64     `bun:"used_count"`
	MaxUsesPerUser int64     `bun:"max_uses_per_user"`
	MinOrderValue  int64     `bun:"min_order_value"`
	IsActive       bool      `bun:"is_active"`
	AppliesTo      string    `bun:"applies_to"`
	ProductIDs     []string  `bun:"product_ids,array"`
	CreatedAt      time.Time `bun:"created_at"`
	UpdatedAt      time.Time `bun:"updated_at"`
}

func rowOf(d *domain.Discount) *discountRow {
	s := d.Snapshot()
	ids := s.ProductIDs
	if ids == nil {
		ids = []string{} // the column is NOT NULL
	}
	return &discountRow{ID: s.ID, ShopID: s.ShopID, Code: s.Code, Name: s.Name, Description: s.Description, Type: string(s.Type),
		Value: s.Value, StartDate: s.StartDate, EndDate: s.EndDate, MaxUses: s.MaxUses, UsedCount: s.UsedCount,
		MaxUsesPerUser: s.MaxUsesPerUser, MinOrderValue: s.MinOrderValue, IsActive: s.IsActive, AppliesTo: string(s.AppliesTo),
		ProductIDs: ids, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

func (r *discountRow) toDomain() *domain.Discount {
	var ids []string
	if len(r.ProductIDs) > 0 {
		ids = r.ProductIDs
	}
	return domain.Restore(domain.DiscountSnapshot{ID: r.ID, ShopID: r.ShopID, Code: r.Code, Name: r.Name, Description: r.Description,
		Type: domain.Type(r.Type), Value: r.Value, StartDate: r.StartDate.UTC(), EndDate: r.EndDate.UTC(), MaxUses: r.MaxUses,
		UsedCount: r.UsedCount, MaxUsesPerUser: r.MaxUsesPerUser, MinOrderValue: r.MinOrderValue, IsActive: r.IsActive,
		AppliesTo: domain.Scope(r.AppliesTo), ProductIDs: ids, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()})
}

type discountRepo struct{ s *pgdb.Store }

func (r *discountRepo) Insert(ctx context.Context, d *domain.Discount) error {
	_, err := r.s.DB(ctx).NewInsert().Model(rowOf(d)).Exec(ctx)
	if pgdb.IsUniqueViolation(err, "uq_discounts_shop_code") {
		return domain.ErrCodeTaken()
	}
	return err
}

// Save writes every mutable field. The caller holds the row lock (a Lock* method of this transaction), so the
// counter it read is the counter it writes back.
func (r *discountRepo) Save(ctx context.Context, d *domain.Discount) error {
	row := rowOf(d)
	res, err := r.s.DB(ctx).NewUpdate().Model(row).
		Column("name", "description", "end_date", "max_uses", "used_count", "max_uses_per_user", "min_order_value", "is_active",
			"applies_to", "product_ids", "updated_at").
		WherePK().Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound()
	}
	return nil
}

func (r *discountRepo) one(ctx context.Context, q *bun.SelectQuery) (*domain.Discount, error) {
	row := new(discountRow)
	err := q.Model(row).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound()
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}

func (r *discountRepo) Find(ctx context.Context, id string) (*domain.Discount, error) {
	return r.one(ctx, r.s.DB(ctx).NewSelect().Where("id = ?", id))
}

func (r *discountRepo) FindByCode(ctx context.Context, ref domain.CodeRef) (*domain.Discount, error) {
	return r.one(ctx, r.s.DB(ctx).NewSelect().Where("shop_id = ?", ref.ShopID).Where("code = ?", ref.Code))
}

func (r *discountRepo) LockByID(ctx context.Context, id string) (*domain.Discount, error) {
	return r.one(ctx, r.s.DB(ctx).NewSelect().Where("id = ?", id).For("UPDATE"))
}

// LockByCodes takes the row locks in (shop, code) order. Two orders that need overlapping codes queue up in the same
// order and cannot deadlock; the second then sees the first one's committed counter.
func (r *discountRepo) LockByCodes(ctx context.Context, refs []domain.CodeRef) (map[domain.CodeRef]*domain.Discount, error) {
	out := map[domain.CodeRef]*domain.Discount{}
	if len(refs) == 0 {
		return out, nil
	}
	var rows []discountRow
	err := r.s.DB(ctx).NewSelect().Model(&rows).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			for _, ref := range refs {
				q = q.WhereOr("(shop_id = ? AND code = ?)", ref.ShopID, ref.Code)
			}
			return q
		}).OrderExpr("shop_id, code").For("UPDATE").Scan(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		out[domain.CodeRef{ShopID: rows[i].ShopID, Code: rows[i].Code}] = rows[i].toDomain()
	}
	return out, nil
}

func (r *discountRepo) LockByIDs(ctx context.Context, ids []string) (map[string]*domain.Discount, error) {
	out := map[string]*domain.Discount{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []discountRow
	if err := r.s.DB(ctx).NewSelect().Model(&rows).Where("id IN (?)", bun.List(ids)).OrderExpr("id").For("UPDATE").Scan(ctx); err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].ID] = rows[i].toDomain()
	}
	return out, nil
}

func (r *discountRepo) ListByShop(ctx context.Context, shopID string, offset, limit int) ([]*domain.Discount, int64, error) {
	var rows []discountRow
	n, err := r.s.DB(ctx).NewSelect().Model(&rows).Where("shop_id = ?", shopID).
		OrderExpr("created_at DESC, id DESC").Offset(int64(offset)).Limit(int64(limit)).ScanAndCount(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]*domain.Discount, len(rows))
	for i := range rows {
		out[i] = rows[i].toDomain()
	}
	return out, int64(n), nil
}
