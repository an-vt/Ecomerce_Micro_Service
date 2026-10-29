package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createAccountsTable{}) }

type createAccountsTable struct{}

func (m *createAccountsTable) Name() string { return "2026_10_30_000010_create_accounts_table" }

func (m *createAccountsTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE accounts (
			id            UUID PRIMARY KEY,
			type          VARCHAR(8)   NOT NULL,
			email         VARCHAR(320) NOT NULL,
			name          VARCHAR(150) NOT NULL,
			password_hash VARCHAR(100),
			status        VARCHAR(16)  NOT NULL DEFAULT 'pending',
			verified      BOOLEAN      NOT NULL DEFAULT FALSE,
			roles         TEXT[]       NOT NULL DEFAULT '{}',
			created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
			CONSTRAINT uq_accounts_type_email UNIQUE (type, email),
			CONSTRAINT ck_accounts_type CHECK (type IN ('SHOP', 'USER'))
		)`)
	return err
}

func (m *createAccountsTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS accounts`)
	return err
}
