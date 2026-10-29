package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type sagaData struct {
	PaymentID     string   `json:"paymentId,omitempty"`
	DiscountTotal int64    `json:"discountTotal,omitempty"`
	Pending       []string `json:"pending,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	Attempts      int      `json:"attempts,omitempty"`
}

type sagaRow struct {
	bun.BaseModel `bun:"table:saga_instances"`

	ID           string    `bun:"id,pk"`
	Type         string    `bun:"type"`
	State        string    `bun:"state"`
	HasDiscounts bool      `bun:"has_discounts"`
	StepDeadline time.Time `bun:"step_deadline_at"`
	Data         sagaData  `bun:"data,type:jsonb"`
	Version      int64     `bun:"version"`
	CreatedAt    time.Time `bun:"created_at"`
	UpdatedAt    time.Time `bun:"updated_at"`
}

func sagaRowOf(s *domain.Saga) *sagaRow {
	n := s.Snapshot()
	return &sagaRow{ID: n.ID, Type: "checkout", State: string(n.State), HasDiscounts: n.HasDiscounts, StepDeadline: n.StepDeadline,
		Data:    sagaData{PaymentID: n.PaymentID, DiscountTotal: n.DiscountTotal, Pending: n.Pending, Reason: n.Reason, Attempts: n.Attempts},
		Version: n.Version, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt}
}

func (r *sagaRow) toDomain() *domain.Saga {
	return domain.RestoreSaga(domain.SagaSnapshot{ID: r.ID, State: domain.SagaState(r.State), HasDiscounts: r.HasDiscounts, StepDeadline: r.StepDeadline.UTC(),
		PaymentID: r.Data.PaymentID, DiscountTotal: r.Data.DiscountTotal, Pending: r.Data.Pending, Reason: r.Data.Reason, Attempts: r.Data.Attempts,
		Version: r.Version, CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()})
}

type sagaRepo struct{ s *pgdb.Store }

func (r *sagaRepo) Insert(ctx context.Context, s *domain.Saga) error {
	_, err := r.s.DB(ctx).NewInsert().Model(sagaRowOf(s)).Exec(ctx)
	return err
}

func (r *sagaRepo) Lock(ctx context.Context, id string) (*domain.Saga, error) {
	row := new(sagaRow)
	err := r.s.DB(ctx).NewSelect().Model(row).Where("id = ?", id).For("UPDATE").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrSagaNotFound()
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}

func (r *sagaRepo) Save(ctx context.Context, s *domain.Saga) error {
	row := sagaRowOf(s)
	res, err := r.s.DB(ctx).NewUpdate().Model(row).Column("state", "step_deadline_at", "data", "version", "updated_at").WherePK().Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrSagaNotFound()
	}
	return nil
}

// LockNextDue takes the most overdue unfinished saga that no other transaction holds. SKIP LOCKED is what lets several
// replicas run the sweeper side by side: each gets a different saga, and a saga being handled is simply not seen.
func (r *sagaRepo) LockNextDue(ctx context.Context, now time.Time) (*domain.Saga, error) {
	row := new(sagaRow)
	err := r.s.DB(ctx).NewSelect().Model(row).
		Where("state NOT IN ('COMPLETED', 'FAILED')").Where("step_deadline_at < ?", now.UTC()).
		OrderExpr("step_deadline_at, id").Limit(1).For("UPDATE SKIP LOCKED").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}
