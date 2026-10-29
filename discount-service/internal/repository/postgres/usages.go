package postgres

import (
	"context"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type usageRow struct {
	bun.BaseModel `bun:"table:discount_usages"`

	ID         string    `bun:"id,pk"`
	DiscountID string    `bun:"discount_id"`
	UserID     string    `bun:"user_id"`
	OrderID    string    `bun:"order_id"`
	Amount     int64     `bun:"amount"`
	Status     string    `bun:"status"`
	CreatedAt  time.Time `bun:"created_at"`
	UpdatedAt  time.Time `bun:"updated_at"`
}

type usageRepo struct{ s *pgdb.Store }

func (r *usageRepo) ForOrder(ctx context.Context, orderID string) ([]*domain.Usage, error) {
	var rows []usageRow
	if err := r.s.DB(ctx).NewSelect().Model(&rows).Where("order_id = ?", orderID).OrderExpr("discount_id").Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]*domain.Usage, len(rows))
	for i, x := range rows {
		out[i] = domain.RestoreUsage(domain.UsageSnapshot{ID: x.ID, DiscountID: x.DiscountID, UserID: x.UserID, OrderID: x.OrderID,
			Amount: x.Amount, Status: domain.UsageStatus(x.Status), CreatedAt: x.CreatedAt.UTC(), UpdatedAt: x.UpdatedAt.UTC()})
	}
	return out, nil
}

func (r *usageRepo) CountApplied(ctx context.Context, userID string, discountIDs []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(discountIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		DiscountID string `bun:"discount_id"`
		N          int64  `bun:"n"`
	}
	err := r.s.DB(ctx).NewSelect().TableExpr("discount_usages").ColumnExpr("discount_id, count(*) AS n").
		Where("user_id = ?", userID).Where("status = 'APPLIED'").Where("discount_id IN (?)", bun.List(discountIDs)).
		GroupExpr("discount_id").Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	for _, x := range rows {
		out[x.DiscountID] = x.N
	}
	return out, nil
}

func (r *usageRepo) Insert(ctx context.Context, us []*domain.Usage) error {
	if len(us) == 0 {
		return nil
	}
	rows := make([]usageRow, len(us))
	for i, u := range us {
		s := u.Snapshot()
		rows[i] = usageRow{ID: s.ID, DiscountID: s.DiscountID, UserID: s.UserID, OrderID: s.OrderID, Amount: s.Amount,
			Status: string(s.Status), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	}
	_, err := r.s.DB(ctx).NewInsert().Model(&rows).Exec(ctx)
	if pgdb.IsUniqueViolation(err, "uq_discount_usages_discount_order") {
		return domain.ErrAlreadyUsed()
	}
	return err
}

func (r *usageRepo) Save(ctx context.Context, u *domain.Usage) error {
	s := u.Snapshot()
	res, err := r.s.DB(ctx).NewUpdate().Model((*usageRow)(nil)).Set("status = ?", string(s.Status)).Set("updated_at = ?", s.UpdatedAt).
		Where("id = ?", s.ID).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound()
	}
	return nil
}
