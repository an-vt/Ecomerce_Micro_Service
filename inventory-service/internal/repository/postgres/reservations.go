package postgres

import (
	"context"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type reservationRow struct {
	bun.BaseModel `bun:"table:reservations"`

	ID        string    `bun:"id,pk"`
	OrderID   string    `bun:"order_id"`
	ProductID string    `bun:"product_id"`
	Quantity  int64     `bun:"quantity"`
	Status    string    `bun:"status"`
	ExpiresAt time.Time `bun:"expires_at"`
	CreatedAt time.Time `bun:"created_at"`
	UpdatedAt time.Time `bun:"updated_at"`
}

func (r *reservationRow) toDomain() *domain.Reservation {
	return domain.RestoreReservation(domain.ReservationSnapshot{ID: r.ID, OrderID: r.OrderID, ProductID: r.ProductID, Quantity: r.Quantity,
		Status: domain.ReservationStatus(r.Status), ExpiresAt: r.ExpiresAt.UTC(), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()})
}

type reservationRepo struct{ s *pgdb.Store }

func (r *reservationRepo) ForOrder(ctx context.Context, orderID string) ([]*domain.Reservation, error) {
	var rows []reservationRow
	if err := r.s.DB(ctx).NewSelect().Model(&rows).Where("order_id = ?", orderID).OrderExpr("product_id").Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]*domain.Reservation, len(rows))
	for i := range rows {
		out[i] = rows[i].toDomain()
	}
	return out, nil
}

func (r *reservationRepo) Insert(ctx context.Context, rs []*domain.Reservation) error {
	if len(rs) == 0 {
		return nil
	}
	rows := make([]reservationRow, len(rs))
	for i, x := range rs {
		s := x.Snapshot()
		rows[i] = reservationRow{ID: s.ID, OrderID: s.OrderID, ProductID: s.ProductID, Quantity: s.Quantity, Status: string(s.Status),
			ExpiresAt: s.ExpiresAt, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	}
	_, err := r.s.DB(ctx).NewInsert().Model(&rows).Exec(ctx)
	if pgdb.IsUniqueViolation(err, "uq_reservations_order_product") {
		return domain.ErrAlreadyReserved()
	}
	return err
}

func (r *reservationRepo) Save(ctx context.Context, x *domain.Reservation) error {
	s := x.Snapshot()
	res, err := r.s.DB(ctx).NewUpdate().Model((*reservationRow)(nil)).Set("status = ?", string(s.Status)).Set("updated_at = ?", s.UpdatedAt).
		Where("id = ?", s.ID).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrInventoryNotFound()
	}
	return nil
}

// ExpiredOrders uses the partial index on active holds. Several replicas may pick the same order; that is safe, because
// the expiry transaction locks the stock rows and re-checks each hold before changing it.
func (r *reservationRepo) ExpiredOrders(ctx context.Context, now time.Time, limit int) ([]string, error) {
	var ids []string
	err := r.s.DB(ctx).NewSelect().TableExpr("reservations").ColumnExpr("DISTINCT order_id").
		Where("status = 'RESERVED'").Where("expires_at <= ?", now.UTC()).OrderExpr("order_id").Limit(int64(limit)).Scan(ctx, &ids)
	return ids, err
}
