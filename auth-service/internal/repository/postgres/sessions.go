package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type sessionRow struct {
	bun.BaseModel `bun:"table:sessions"`

	ID                string     `bun:"id,pk"`
	AccountID         string     `bun:"account_id"`
	RefreshTokenHash  string     `bun:"refresh_token_hash"`
	UsedRefreshHashes []string   `bun:"used_refresh_hashes,array"`
	ExpiresAt         time.Time  `bun:"expires_at"`
	RevokedAt         *time.Time `bun:"revoked_at"`
	RevokeReason      *string    `bun:"revoke_reason"`
	CreatedAt         time.Time  `bun:"created_at"`
	UpdatedAt         time.Time  `bun:"updated_at"`
}

func toSessionRow(s *domain.Session) *sessionRow {
	sn := s.Snapshot()
	r := &sessionRow{ID: sn.ID, AccountID: sn.AccountID, RefreshTokenHash: sn.RefreshHash, UsedRefreshHashes: sn.UsedHashes,
		ExpiresAt: sn.ExpiresAt, RevokedAt: sn.RevokedAt, CreatedAt: sn.CreatedAt, UpdatedAt: sn.UpdatedAt}
	if r.UsedRefreshHashes == nil {
		r.UsedRefreshHashes = []string{}
	}
	if sn.RevokeReason != "" {
		r.RevokeReason = &sn.RevokeReason
	}
	return r
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func (r *sessionRow) toDomain() *domain.Session {
	reason := ""
	if r.RevokeReason != nil {
		reason = *r.RevokeReason
	}
	return domain.RestoreSession(domain.SessionSnapshot{ID: r.ID, AccountID: r.AccountID, RefreshHash: r.RefreshTokenHash,
		UsedHashes: r.UsedRefreshHashes, ExpiresAt: r.ExpiresAt.UTC(), RevokedAt: utcPtr(r.RevokedAt), RevokeReason: reason,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()})
}

type sessionRepo struct{ s *pgdb.Store }

func (r *sessionRepo) Create(ctx context.Context, s *domain.Session) error {
	_, err := r.s.DB(ctx).NewInsert().Model(toSessionRow(s)).Exec(ctx)
	return err
}

func (r *sessionRepo) one(ctx context.Context, notFound error, where string, arg any) (*domain.Session, error) {
	row := new(sessionRow)
	err := r.s.DB(ctx).NewSelect().Model(row).Where(where, arg).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}

func (r *sessionRepo) FindByID(ctx context.Context, id string) (*domain.Session, error) {
	return r.one(ctx, errs.NotFound("SESSION_NOT_FOUND", "session not found"), "id = ?", id)
}

func (r *sessionRepo) FindByRefreshHash(ctx context.Context, hash string) (*domain.Session, error) {
	return r.one(ctx, domain.ErrTokenInvalid(), "refresh_token_hash = ?", hash)
}

// FindByUsedHash uses the GIN index on used_refresh_hashes.
func (r *sessionRepo) FindByUsedHash(ctx context.Context, hash string) (*domain.Session, error) {
	return r.one(ctx, domain.ErrTokenInvalid(), "used_refresh_hashes @> ?", pgdialect.Array([]string{hash}))
}

// Rotate is a compare-and-swap on the refresh hash: of two concurrent refreshes with one token, one row update
// succeeds and the other affects zero rows.
func (r *sessionRepo) Rotate(ctx context.Context, s *domain.Session, oldHash string) error {
	row := toSessionRow(s)
	res, err := r.s.DB(ctx).NewUpdate().Model(row).
		Column("refresh_token_hash", "used_refresh_hashes", "expires_at", "updated_at").
		Where("id = ?", row.ID).Where("refresh_token_hash = ?", oldHash).Where("revoked_at IS NULL").Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrTokenInvalid()
	}
	return nil
}

func (r *sessionRepo) Revoke(ctx context.Context, s *domain.Session) error {
	row := toSessionRow(s)
	res, err := r.s.DB(ctx).NewUpdate().Model(row).Column("revoked_at", "revoke_reason", "updated_at").WherePK().Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrTokenInvalid()
	}
	return nil
}
