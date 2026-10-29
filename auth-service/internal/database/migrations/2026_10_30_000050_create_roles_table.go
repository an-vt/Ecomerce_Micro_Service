package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createRolesTable{}) }

type createRolesTable struct{}

func (m *createRolesTable) Name() string { return "2026_10_30_000050_create_roles_table" }

func (m *createRolesTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE roles (
			id          UUID PRIMARY KEY,
			name        VARCHAR(32)  NOT NULL,
			slug        VARCHAR(64)  NOT NULL,
			status      VARCHAR(8)   NOT NULL DEFAULT 'active',
			description TEXT         NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
			CONSTRAINT uq_roles_name UNIQUE (name),
			CONSTRAINT ck_roles_status CHECK (status IN ('active', 'block', 'pending'))
		)`)
	return err
}

func (m *createRolesTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS roles`)
	return err
}
