package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&createShopsCollection{}) }

type createShopsCollection struct{}

func (m *createShopsCollection) Name() string { return "2026_10_30_000050_create_shops_collection" }

func (m *createShopsCollection) Up(ctx context.Context, db *mongo.Database) error {
	return mongomigrate.EnsureCollection(ctx, db, "shops")
}

func (m *createShopsCollection) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection("shops").Drop(ctx)
}
