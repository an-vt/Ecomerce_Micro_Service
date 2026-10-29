package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createInventoriesTable{}) }

type createInventoriesTable struct{}

func (m *createInventoriesTable) Name() string { return "2026_10_30_000010_create_inventories_table" }

func (m *createInventoriesTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE inventories (
			id         UUID PRIMARY KEY,
			product_id VARCHAR(64)  NOT NULL,
			shop_id    VARCHAR(64)  NOT NULL,
			location   VARCHAR(255) NOT NULL DEFAULT 'unknown',
			stock      BIGINT       NOT NULL DEFAULT 0,
			reserved   BIGINT       NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
			CONSTRAINT uq_inventories_product UNIQUE (product_id),
			CONSTRAINT ck_inventories_stock CHECK (stock >= 0),
			CONSTRAINT ck_inventories_reserved CHECK (reserved >= 0)
		)`)
	return err
}

func (m *createInventoriesTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS inventories`)
	return err
}
