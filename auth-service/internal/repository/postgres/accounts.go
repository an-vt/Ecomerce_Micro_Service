package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/principal"
)

type accountRow struct {
	bun.BaseModel `bun:"table:accounts"`

	ID           string    `bun:"id,pk"`
	Type         string    `bun:"type"`
	Email        string    `bun:"email"`
	Name         string    `bun:"name"`
	PasswordHash *string   `bun:"password_hash"`
	Status       string    `bun:"status"`
	Verified     bool      `bun:"verified"`
	Roles        []string  `bun:"roles,array"`
	CreatedAt    time.Time `bun:"created_at"`
	UpdatedAt    time.Time `bun:"updated_at"`
}

func toAccountRow(a *domain.Account) *accountRow {
	s := a.Snapshot()
	r := &accountRow{ID: s.ID, Type: string(s.Type), Email: s.Email.String(), Name: s.Name, Status: string(s.Status),
		Verified: s.Verified, Roles: s.Roles, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	if s.PasswordHash != "" {
		r.PasswordHash = &s.PasswordHash
	}
	if r.Roles == nil {
		r.Roles = []string{}
	}
	return r
}

func (r *accountRow) toDomain() *domain.Account {
	hash := ""
	if r.PasswordHash != nil {
		hash = *r.PasswordHash
	}
	return domain.RestoreAccount(domain.AccountSnapshot{ID: r.ID, Type: principal.Type(r.Type), Email: domain.Email(r.Email), Name: r.Name,
		PasswordHash: hash, Status: domain.Status(r.Status), Verified: r.Verified, Roles: r.Roles,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC()})
}

type accountRepo struct{ s *pgdb.Store }

func (r *accountRepo) Create(ctx context.Context, a *domain.Account) error {
	_, err := r.s.DB(ctx).NewInsert().Model(toAccountRow(a)).Exec(ctx)
	if pgdb.IsUniqueViolation(err, "uq_accounts_type_email") {
		return domain.ErrEmailTaken()
	}
	return err
}

func (r *accountRepo) one(ctx context.Context, q func(*bun.SelectQuery) *bun.SelectQuery) (*domain.Account, error) {
	row := new(accountRow)
	err := q(r.s.DB(ctx).NewSelect().Model(row)).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrAccountNotFound()
	}
	if err != nil {
		return nil, err
	}
	return row.toDomain(), nil
}

func (r *accountRepo) FindByID(ctx context.Context, id string) (*domain.Account, error) {
	return r.one(ctx, func(q *bun.SelectQuery) *bun.SelectQuery { return q.Where("id = ?", id) })
}

func (r *accountRepo) FindByEmail(ctx context.Context, typ principal.Type, email domain.Email) (*domain.Account, error) {
	return r.one(ctx, func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("type = ?", string(typ)).Where("email = ?", email.String())
	})
}

func (r *accountRepo) Update(ctx context.Context, a *domain.Account) error {
	row := toAccountRow(a)
	res, err := r.s.DB(ctx).NewUpdate().Model(row).
		Column("name", "password_hash", "status", "verified", "roles", "updated_at").WherePK().Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrAccountNotFound()
	}
	return nil
}
