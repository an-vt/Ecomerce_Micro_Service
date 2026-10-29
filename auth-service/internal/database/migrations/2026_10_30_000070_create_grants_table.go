package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createGrantsTable{}) }

type createGrantsTable struct{}

func (m *createGrantsTable) Name() string { return "2026_10_30_000070_create_grants_table" }

func (m *createGrantsTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE grants (
			id          UUID PRIMARY KEY,
			role_id     UUID         NOT NULL REFERENCES roles (id) ON DELETE CASCADE,
			resource_id UUID         NOT NULL REFERENCES resources (id) ON DELETE CASCADE,
			actions     TEXT[]       NOT NULL,
			attributes  VARCHAR(255) NOT NULL DEFAULT '*',
			CONSTRAINT uq_grants_role_resource UNIQUE (role_id, resource_id)
		)`)
	return err
}

func (m *createGrantsTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS grants`)
	return err
}
