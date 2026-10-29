package seeders

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/database/seeder"
)

func init() { mongomigrate.RegisterSeeder(&DatabaseSeeder{}) }

// DatabaseSeeder is the entry point of `console db:seed`.
type DatabaseSeeder struct{}

func (s *DatabaseSeeder) Name() string { return seeder.DatabaseSeeder }

func (s *DatabaseSeeder) Run(ctx context.Context, db *mongo.Database, set *seeder.Set[*mongo.Database]) error {
	return set.Call(ctx, db, "TemplateSeeder")
}
