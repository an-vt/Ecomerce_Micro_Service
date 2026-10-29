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

type apiKeyRow struct {
	bun.BaseModel `bun:"table:api_keys"`

	ID          string    `bun:"id,pk"`
	Name        string    `bun:"name"`
	KeyHash     string    `bun:"key_hash"`
	Permissions []string  `bun:"permissions,array"`
	Active      bool      `bun:"active"`
	CreatedAt   time.Time `bun:"created_at,default:now()"`
}

type apiKeyRepo struct{ s *pgdb.Store }

func (r *apiKeyRepo) FindByHash(ctx context.Context, hash string) (*domain.ApiKey, error) {
	row := new(apiKeyRow)
	err := r.s.DB(ctx).NewSelect().Model(row).Where("key_hash = ?", hash).Limit(1).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.ApiKey{ID: row.ID, Name: row.Name, KeyHash: row.KeyHash, Permissions: row.Permissions, Active: row.Active}, nil
}

func (r *apiKeyRepo) Save(ctx context.Context, k *domain.ApiKey) error {
	perms := k.Permissions
	if perms == nil {
		perms = []string{}
	}
	_, err := r.s.DB(ctx).NewInsert().Model(&apiKeyRow{ID: k.ID, Name: k.Name, KeyHash: k.KeyHash, Permissions: perms, Active: k.Active}).
		On("CONFLICT (key_hash) DO UPDATE").
		Set("name = EXCLUDED.name").Set("permissions = EXCLUDED.permissions").Set("active = EXCLUDED.active").Exec(ctx)
	return err
}
