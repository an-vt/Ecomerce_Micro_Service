package pgoutbox

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

// CreateTables returns the migration that creates outbox_messages and inbox_messages. Every PostgreSQL service
// registers it from a one-line migration file:
//
//	func init() { pgmigrate.Register(pgoutbox.CreateTables("2026_10_30_000090_create_outbox_inbox_tables")) }
func CreateTables(name string) pgmigrate.Migration { return createTables(name) }

type createTables string

func (m createTables) Name() string { return string(m) }

func (m createTables) Up(ctx context.Context, db bun.IDB) error {
	for _, stmt := range []string{
		`CREATE TABLE outbox_messages (
			id           VARCHAR(64) PRIMARY KEY,
			topic        VARCHAR(255) NOT NULL,
			key          VARCHAR(255) NOT NULL,
			type         VARCHAR(255) NOT NULL,
			payload      JSONB NOT NULL,
			headers      JSONB NOT NULL DEFAULT '{}',
			status       VARCHAR(8) NOT NULL DEFAULT 'PENDING',
			attempts     INT NOT NULL DEFAULT 0,
			available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			sent_at      TIMESTAMPTZ
		)`,
		`CREATE INDEX idx_outbox_due ON outbox_messages (available_at, created_at) WHERE status = 'PENDING'`,
		`CREATE INDEX idx_outbox_sent ON outbox_messages (sent_at) WHERE status = 'SENT'`,
		`CREATE TABLE inbox_messages (
			message_id   VARCHAR(64) NOT NULL,
			consumer     VARCHAR(128) NOT NULL,
			processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (message_id, consumer)
		)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (m createTables) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS inbox_messages; DROP TABLE IF EXISTS outbox_messages`)
	return err
}
