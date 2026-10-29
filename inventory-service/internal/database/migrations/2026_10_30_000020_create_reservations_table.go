package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createReservationsTable{}) }

type createReservationsTable struct{}

func (m *createReservationsTable) Name() string { return "2026_10_30_000020_create_reservations_table" }

func (m *createReservationsTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE reservations (
			id         UUID PRIMARY KEY,
			order_id   VARCHAR(64) NOT NULL,
			product_id VARCHAR(64) NOT NULL,
			quantity   BIGINT      NOT NULL,
			status     VARCHAR(16) NOT NULL DEFAULT 'RESERVED',
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			CONSTRAINT uq_reservations_order_product UNIQUE (order_id, product_id),
			CONSTRAINT ck_reservations_quantity CHECK (quantity > 0),
			CONSTRAINT ck_reservations_status CHECK (status IN ('RESERVED', 'COMMITTED', 'RELEASED', 'EXPIRED'))
		);
		CREATE INDEX idx_reservations_due ON reservations (expires_at) WHERE status = 'RESERVED'`)
	return err
}

func (m *createReservationsTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS reservations`)
	return err
}
