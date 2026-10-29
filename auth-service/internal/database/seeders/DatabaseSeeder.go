package seeders

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/database/seeder"
)

func init() { pgmigrate.RegisterSeeder(&DatabaseSeeder{}) }

// DatabaseSeeder is the entry point of `console db:seed`: reference data first, then the accounts that depend on it.
type DatabaseSeeder struct{}

func (s *DatabaseSeeder) Name() string { return seeder.DatabaseSeeder }

func (s *DatabaseSeeder) Run(ctx context.Context, db bun.IDB, set *seeder.Set[bun.IDB]) error {
	return set.Call(ctx, db, "RbacSeeder", "AdminSeeder", "ApiKeySeeder")
}
