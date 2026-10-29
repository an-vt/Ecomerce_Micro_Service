package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createApiKeysTable{}) }

type createApiKeysTable struct{}

func (m *createApiKeysTable) Name() string { return "2026_10_30_000040_create_api_keys_table" }

func (m *createApiKeysTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE api_keys (
			id          UUID PRIMARY KEY,
			name        VARCHAR(100) NOT NULL,
			key_hash    VARCHAR(64)  NOT NULL,
			permissions TEXT[]       NOT NULL DEFAULT '{}',
			active      BOOLEAN      NOT NULL DEFAULT TRUE,
			created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
			CONSTRAINT uq_api_keys_key_hash UNIQUE (key_hash)
		)`)
	return err
}

func (m *createApiKeysTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS api_keys`)
	return err
}
