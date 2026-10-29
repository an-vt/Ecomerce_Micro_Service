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

type keyRow struct {
	bun.BaseModel `bun:"table:idempotency_keys"`

	UserID      string    `bun:"user_id,pk"`
	Key         string    `bun:"key,pk"`
	RequestHash string    `bun:"request_hash"`
	OrderID     string    `bun:"order_id"`
	CreatedAt   time.Time `bun:"created_at"`
}

type keyRepo struct{ s *pgdb.Store }

// Claim uses ON CONFLICT DO NOTHING rather than catching the unique violation: in PostgreSQL a violation aborts the
// whole transaction, and the caller still has to answer. When another transaction is claiming the same key, the
// statement waits for it to commit or roll back before deciding.
func (r *keyRepo) Claim(ctx context.Context, rec domain.IdempotencyRecord) (bool, error) {
	res, err := r.s.DB(ctx).NewInsert().Model(&keyRow{UserID: rec.UserID, Key: rec.Key, RequestHash: rec.RequestHash, OrderID: rec.OrderID, CreatedAt: rec.CreatedAt}).
		On("CONFLICT (user_id, key) DO NOTHING").Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *keyRepo) Find(ctx context.Context, userID, key string) (*domain.IdempotencyRecord, error) {
	row := new(keyRow)
	err := r.s.DB(ctx).NewSelect().Model(row).Where("user_id = ?", userID).Where("key = ?", key).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.IdempotencyRecord{UserID: row.UserID, Key: row.Key, RequestHash: row.RequestHash, OrderID: row.OrderID, CreatedAt: row.CreatedAt.UTC()}, nil
}
