package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createSagaInstancesTable{}) }

type createSagaInstancesTable struct{}

func (m *createSagaInstancesTable) Name() string {
	return "2026_10_30_000030_create_saga_instances_table"
}

func (m *createSagaInstancesTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE saga_instances (
			id               UUID PRIMARY KEY REFERENCES orders (id),
			type             VARCHAR(16) NOT NULL DEFAULT 'checkout',
			state            VARCHAR(24) NOT NULL,
			has_discounts    BOOLEAN     NOT NULL,
			step_deadline_at TIMESTAMPTZ NOT NULL,
			data             JSONB       NOT NULL DEFAULT '{}',
			version          BIGINT      NOT NULL DEFAULT 0,
			created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
			CONSTRAINT ck_saga_state CHECK (state IN
				('RESERVING_STOCK', 'APPLYING_DISCOUNT', 'PROCESSING_PAYMENT', 'COMMITTING_STOCK', 'COMPLETED', 'COMPENSATING', 'FAILED'))
		);
		-- the sweeper only ever looks at unfinished sagas
		CREATE INDEX idx_saga_due ON saga_instances (step_deadline_at) WHERE state NOT IN ('COMPLETED', 'FAILED')`)
	return err
}

func (m *createSagaInstancesTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS saga_instances`)
	return err
}
