package migrations

import (
	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/outbox/mongooutbox"
)

func init() {
	mongomigrate.Register(mongooutbox.CreateCollections("2026_10_30_000090_create_outbox_inbox_collections"))
}
