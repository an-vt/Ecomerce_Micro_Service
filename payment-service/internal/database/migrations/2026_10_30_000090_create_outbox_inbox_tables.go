package migrations

import (
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/outbox/pgoutbox"
)

func init() {
	pgmigrate.Register(pgoutbox.CreateTables("2026_10_30_000090_create_outbox_inbox_tables"))
}
