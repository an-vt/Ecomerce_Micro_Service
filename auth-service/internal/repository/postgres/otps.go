package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type otpRow struct {
	bun.BaseModel `bun:"table:otps"`

	ID        string     `bun:"id,pk"`
	Email     string     `bun:"email"`
	TokenHash string     `bun:"token_hash"`
	ExpiresAt time.Time  `bun:"expires_at"`
	UsedAt    *time.Time `bun:"used_at"`
	CreatedAt time.Time  `bun:"created_at"`
}

type otpRepo struct{ s *pgdb.Store }

func (r *otpRepo) Create(ctx context.Context, o *domain.Otp) error {
	sn := o.Snapshot()
	_, err := r.s.DB(ctx).NewInsert().Model(&otpRow{ID: sn.ID, Email: sn.Email.String(), TokenHash: sn.TokenHash,
		ExpiresAt: sn.ExpiresAt, UsedAt: sn.UsedAt, CreatedAt: sn.CreatedAt}).Exec(ctx)
	return err
}

func (r *otpRepo) FindByTokenHash(ctx context.Context, hash string) (*domain.Otp, error) {
	row := new(otpRow)
	err := r.s.DB(ctx).NewSelect().Model(row).Where("token_hash = ?", hash).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrTokenInvalid()
	}
	if err != nil {
		return nil, err
	}
	return domain.RestoreOtp(domain.OtpSnapshot{ID: row.ID, Email: domain.Email(row.Email), TokenHash: row.TokenHash,
		ExpiresAt: row.ExpiresAt.UTC(), UsedAt: utcPtr(row.UsedAt), CreatedAt: row.CreatedAt.UTC()}), nil
}

// MarkUsed is a compare-and-swap: only the first request to consume a token updates a row.
func (r *otpRepo) MarkUsed(ctx context.Context, o *domain.Otp) error {
	sn := o.Snapshot()
	res, err := r.s.DB(ctx).NewUpdate().Model((*otpRow)(nil)).Set("used_at = ?", sn.UsedAt).
		Where("id = ?", sn.ID).Where("used_at IS NULL").Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrTokenInvalid()
	}
	return nil
}
