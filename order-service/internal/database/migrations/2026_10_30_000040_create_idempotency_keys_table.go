package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createIdempotencyKeysTable{}) }

type createIdempotencyKeysTable struct{}

func (m *createIdempotencyKeysTable) Name() string {
	return "2026_10_30_000040_create_idempotency_keys_table"
}

func (m *createIdempotencyKeysTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE idempotency_keys (
			user_id      VARCHAR(64)  NOT NULL,
			key          VARCHAR(128) NOT NULL,
			request_hash VARCHAR(64)  NOT NULL,
			order_id     UUID         NOT NULL,
			created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
			PRIMARY KEY (user_id, key)
		)`)
	return err
}

func (m *createIdempotencyKeysTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS idempotency_keys`)
	return err
}
