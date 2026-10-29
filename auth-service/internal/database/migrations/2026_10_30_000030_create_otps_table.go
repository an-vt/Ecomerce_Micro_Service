package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createOtpsTable{}) }

type createOtpsTable struct{}

func (m *createOtpsTable) Name() string { return "2026_10_30_000030_create_otps_table" }

func (m *createOtpsTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE otps (
			id         UUID PRIMARY KEY,
			email      VARCHAR(320) NOT NULL,
			token_hash VARCHAR(64)  NOT NULL,
			expires_at TIMESTAMPTZ  NOT NULL,
			used_at    TIMESTAMPTZ,
			created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
			CONSTRAINT uq_otps_token_hash UNIQUE (token_hash)
		)`)
	return err
}

func (m *createOtpsTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS otps`)
	return err
}
