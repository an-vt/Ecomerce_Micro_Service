package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createSessionsTable{}) }

type createSessionsTable struct{}

func (m *createSessionsTable) Name() string { return "2026_10_30_000020_create_sessions_table" }

func (m *createSessionsTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE sessions (
			id                  UUID PRIMARY KEY,
			account_id          UUID        NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
			refresh_token_hash  VARCHAR(64) NOT NULL,
			used_refresh_hashes TEXT[]      NOT NULL DEFAULT '{}',
			expires_at          TIMESTAMPTZ NOT NULL,
			revoked_at          TIMESTAMPTZ,
			revoke_reason       VARCHAR(32),
			created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
			CONSTRAINT uq_sessions_refresh_hash UNIQUE (refresh_token_hash)
		);
		CREATE INDEX idx_sessions_account ON sessions (account_id);
		CREATE INDEX idx_sessions_used_hashes ON sessions USING GIN (used_refresh_hashes)`)
	return err
}

func (m *createSessionsTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS sessions`)
	return err
}
