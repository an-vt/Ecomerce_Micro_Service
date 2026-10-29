package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&createTemplatesCollection{}) }

type createTemplatesCollection struct{}

func (m *createTemplatesCollection) Name() string {
	return "2026_10_30_000020_create_templates_collection"
}

func (m *createTemplatesCollection) Up(ctx context.Context, db *mongo.Database) error {
	if err := mongomigrate.EnsureCollection(ctx, db, "templates"); err != nil {
		return err
	}
	_, err := db.Collection("templates").Indexes().CreateOne(ctx,
		mongo.IndexModel{Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uq_templates_name")})
	return err
}

func (m *createTemplatesCollection) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection("templates").Drop(ctx)
}
