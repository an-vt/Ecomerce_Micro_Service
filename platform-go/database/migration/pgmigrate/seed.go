package pgmigrate

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/console"
	"github.com/an-vt/ecom/platform-go/database/migration"
	"github.com/an-vt/ecom/platform-go/database/seeder"
)

// Seeder is a PostgreSQL seeder.
type Seeder = seeder.Seeder[bun.IDB]

// Seeders is the registry of the service; seeder files call RegisterSeeder from init().
var Seeders = &seeder.Set[bun.IDB]{}

// RegisterSeeder adds a seeder to the service registry.
func RegisterSeeder(s Seeder) { Seeders.Register(s) }

type seedRunner struct {
	db  *bun.DB
	set *seeder.Set[bun.IDB]
}

func (r seedRunner) Seed(ctx context.Context, class string) ([]string, error) {
	return r.set.Run(ctx, r.db, class)
}

// Console returns the migrator and seeder that the `console` binary hands to console.Run.
func Console(db *bun.DB, dbName string) (console.Migrator, console.Seeder) {
	return NewRunner(db, dbName, Migrations), seedRunner{db: db, set: Seeders}
}

var _ console.Migrator = (*migration.Runner[bun.IDB])(nil)
