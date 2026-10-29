package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createDiscountUsagesTable{}) }

type createDiscountUsagesTable struct{}

func (m *createDiscountUsagesTable) Name() string {
	return "2026_10_30_000020_create_discount_usages_table"
}

func (m *createDiscountUsagesTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE discount_usages (
			id          UUID PRIMARY KEY,
			discount_id UUID        NOT NULL REFERENCES discounts (id),
			user_id     VARCHAR(64) NOT NULL,
			order_id    VARCHAR(64) NOT NULL,
			amount      BIGINT      NOT NULL,
			status      VARCHAR(16) NOT NULL DEFAULT 'APPLIED',
			created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			-- one redemption per discount and order: a redelivered ApplyDiscounts cannot count twice
			CONSTRAINT uq_discount_usages_discount_order UNIQUE (discount_id, order_id),
			CONSTRAINT ck_discount_usages_amount CHECK (amount >= 0),
			CONSTRAINT ck_discount_usages_status CHECK (status IN ('APPLIED', 'RELEASED'))
		);
		CREATE INDEX idx_discount_usages_user ON discount_usages (discount_id, user_id) WHERE status = 'APPLIED';
		CREATE INDEX idx_discount_usages_order ON discount_usages (order_id)`)
	return err
}

func (m *createDiscountUsagesTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS discount_usages`)
	return err
}
