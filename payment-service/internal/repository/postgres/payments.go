// Package postgres implements the payment repository with bun (docs/04-data-model.md §7). Every method takes its
// connection from pgdb.Store.DB(ctx), so it joins the surrounding transaction.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type paymentRow struct {
	bun.BaseModel `bun:"table:payments"`

	ID            string    `bun:"id,pk"`
	OrderID       string    `bun:"order_id"`
	UserID        string    `bun:"user_id"`
	Amount        int64     `bun:"amount"`
	Currency      string    `bun:"currency"`
	Method        string    `bun:"method"`
	Status        string    `bun:"status"`
	ProviderRef   *string   `bun:"provider_ref"`
	FailureReason *string   `bun:"failure_reason"`
	CreatedAt     time.Time `bun:"created_at"`
	UpdatedAt     time.Time `bun:"updated_at"`
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func rowOf(p *domain.Payment) *paymentRow {
	s := p.Snapshot()
	return &paymentRow{ID: s.ID, OrderID: s.OrderID, UserID: s.UserID, Amount: s.Amount, Currency: s.Currency, Method: s.Method,
		Status: string(s.Status), ProviderRef: nullable(s.ProviderRef), FailureReason: nullable(s.FailureReason), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

func (r *paymentRow) toDomain() *domain.Payment {
	s := domain.Snapshot{ID: r.ID, OrderID: r.OrderID, UserID: r.UserID, Amount: r.Amount, Currency: r.Currency, Method: r.Method,
		Status: domain.Status(r.Status), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()}
	if r.ProviderRef != nil {
		s.ProviderRef = *r.ProviderRef
	}
	if r.FailureReason != nil {
		s.FailureReason = *r.FailureReason
	}
	return domain.Restore(s)
}

// Payments implements domain.PaymentRepository.
type Payments struct{ s *pgdb.Store }

// New creates the repository over a store.
func New(s *pgdb.Store) *Payments { return &Payments{s: s} }

// Insert uses ON CONFLICT DO NOTHING rather than catching the unique violation: in PostgreSQL a violation aborts the
// whole transaction, and the caller still has a reply to write. When another transaction is inserting the same order,
// the statement waits for it to commit or roll back before deciding.
func (r *Payments) Insert(ctx context.Context, p *domain.Payment) (bool, error) {
	res, err := r.s.DB(ctx).NewInsert().Model(rowOf(p)).On("CONFLICT (order_id) DO NOTHING").Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// LockByOrder implements domain.PaymentRepository.
func (r *Payments) LockByOrder(ctx context.Context, orderID string) (*domain.Payment, error) {
	row := new(paymentRow)
	err := r.s.DB(ctx).NewSelect().Model(row).Where("order_id = ?", orderID).For("UPDATE").Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound()
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}

// Save implements domain.PaymentRepository.
func (r *Payments) Save(ctx context.Context, p *domain.Payment) error {
	row := rowOf(p)
	res, err := r.s.DB(ctx).NewUpdate().Model(row).Column("status", "provider_ref", "failure_reason", "updated_at").Where("order_id = ?", row.OrderID).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound()
	}
	return nil
}
