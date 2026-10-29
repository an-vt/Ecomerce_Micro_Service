package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createOrderItemsTable{}) }

type createOrderItemsTable struct{}

func (m *createOrderItemsTable) Name() string { return "2026_10_30_000020_create_order_items_table" }

func (m *createOrderItemsTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE order_items (
			id         UUID PRIMARY KEY,
			order_id   UUID         NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
			position   INT          NOT NULL,
			product_id VARCHAR(64)  NOT NULL,
			shop_id    VARCHAR(64)  NOT NULL,
			name       VARCHAR(255) NOT NULL,
			price      BIGINT       NOT NULL,
			quantity   BIGINT       NOT NULL,
			CONSTRAINT uq_order_items_position UNIQUE (order_id, position),
			CONSTRAINT ck_order_items_values CHECK (price >= 0 AND quantity > 0)
		)`)
	return err
}

func (m *createOrderItemsTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS order_items`)
	return err
}
