package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createOrdersTable{}) }

type createOrdersTable struct{}

func (m *createOrdersTable) Name() string { return "2026_10_30_000010_create_orders_table" }

func (m *createOrdersTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE orders (
			id              UUID PRIMARY KEY,
			user_id         VARCHAR(64)  NOT NULL,
			status          VARCHAR(24)  NOT NULL,
			total_price     BIGINT       NOT NULL,
			total_discount  BIGINT       NOT NULL DEFAULT 0,
			fee_ship        BIGINT       NOT NULL DEFAULT 0,
			total_checkout  BIGINT       NOT NULL,
			shipping        JSONB        NOT NULL,
			payment_method  VARCHAR(32)  NOT NULL,
			payment_token   VARCHAR(255) NOT NULL,
			discounts       JSONB        NOT NULL DEFAULT '[]',
			tracking_number VARCHAR(64),
			cancel_reason   VARCHAR(64),
			version         BIGINT       NOT NULL DEFAULT 0,
			created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
			CONSTRAINT ck_orders_status CHECK (status IN
				('PENDING', 'STOCK_RESERVED', 'DISCOUNT_APPLIED', 'PAYMENT_PROCESSING', 'CONFIRMED', 'CANCELLING', 'CANCELLED')),
			CONSTRAINT ck_orders_amounts CHECK (total_price >= 0 AND total_discount >= 0 AND total_discount <= total_price
				AND fee_ship >= 0 AND total_checkout >= 0)
		);
		CREATE INDEX idx_orders_user_created ON orders (user_id, created_at DESC, id DESC)`)
	return err
}

func (m *createOrdersTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS orders`)
	return err
}
