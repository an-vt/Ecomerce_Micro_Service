package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createPaymentsTable{}) }

type createPaymentsTable struct{}

func (m *createPaymentsTable) Name() string { return "2026_10_30_000010_create_payments_table" }

func (m *createPaymentsTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE payments (
			id             UUID PRIMARY KEY,
			order_id       VARCHAR(64)  NOT NULL,
			user_id        VARCHAR(64)  NOT NULL DEFAULT '',
			amount         BIGINT       NOT NULL DEFAULT 0,
			currency       CHAR(3)      NOT NULL,
			method         VARCHAR(32)  NOT NULL DEFAULT '',
			status         VARCHAR(16)  NOT NULL,
			provider_ref   VARCHAR(128),
			failure_reason VARCHAR(64),
			created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
			-- one payment per order: the database, not the application, is what prevents a double charge
			CONSTRAINT uq_payments_order UNIQUE (order_id),
			CONSTRAINT ck_payments_amount CHECK (amount >= 0),
			CONSTRAINT ck_payments_status CHECK (status IN ('PENDING', 'SUCCEEDED', 'FAILED', 'REFUNDED', 'CANCELLED'))
		)`)
	return err
}

func (m *createPaymentsTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS payments`)
	return err
}
