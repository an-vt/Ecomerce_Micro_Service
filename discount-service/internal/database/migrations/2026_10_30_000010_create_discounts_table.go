package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createDiscountsTable{}) }

type createDiscountsTable struct{}

func (m *createDiscountsTable) Name() string { return "2026_10_30_000010_create_discounts_table" }

func (m *createDiscountsTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE discounts (
			id                UUID PRIMARY KEY,
			shop_id           VARCHAR(64)  NOT NULL,
			code              VARCHAR(32)  NOT NULL,
			name              VARCHAR(255) NOT NULL,
			description       TEXT         NOT NULL DEFAULT '',
			type              VARCHAR(16)  NOT NULL,
			value             BIGINT       NOT NULL,
			start_date        TIMESTAMPTZ  NOT NULL,
			end_date          TIMESTAMPTZ  NOT NULL,
			max_uses          BIGINT       NOT NULL,
			used_count        BIGINT       NOT NULL DEFAULT 0,
			max_uses_per_user BIGINT       NOT NULL DEFAULT 0,
			min_order_value   BIGINT       NOT NULL DEFAULT 0,
			is_active         BOOLEAN      NOT NULL DEFAULT TRUE,
			applies_to        VARCHAR(16)  NOT NULL DEFAULT 'all',
			product_ids       TEXT[]       NOT NULL DEFAULT '{}',
			created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
			CONSTRAINT uq_discounts_shop_code UNIQUE (shop_id, code),
			CONSTRAINT ck_discounts_type CHECK (type IN ('fixed_amount', 'percent')),
			CONSTRAINT ck_discounts_value CHECK (value > 0 AND (type <> 'percent' OR value <= 100)),
			CONSTRAINT ck_discounts_dates CHECK (end_date > start_date),
			CONSTRAINT ck_discounts_applies_to CHECK (applies_to IN ('all', 'specific')),
			CONSTRAINT ck_discounts_limits CHECK (max_uses >= 1 AND max_uses_per_user >= 0 AND min_order_value >= 0),
			-- the backstop of the redemption rule: even a bug cannot hand out more uses than the shop allowed
			CONSTRAINT ck_discounts_used_count CHECK (used_count >= 0 AND used_count <= max_uses)
		);
		CREATE INDEX idx_discounts_shop_created ON discounts (shop_id, created_at DESC, id DESC)`)
	return err
}

func (m *createDiscountsTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS discounts`)
	return err
}
