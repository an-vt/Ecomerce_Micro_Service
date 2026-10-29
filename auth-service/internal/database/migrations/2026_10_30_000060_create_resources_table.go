package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createResourcesTable{}) }

type createResourcesTable struct{}

func (m *createResourcesTable) Name() string { return "2026_10_30_000060_create_resources_table" }

func (m *createResourcesTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE resources (
			id          UUID PRIMARY KEY,
			name        VARCHAR(32) NOT NULL,
			slug        VARCHAR(64) NOT NULL,
			description TEXT        NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			CONSTRAINT uq_resources_name UNIQUE (name)
		)`)
	return err
}

func (m *createResourcesTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS resources`)
	return err
}
