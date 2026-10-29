package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&createSpusCollection{}) }

type createSpusCollection struct{}

func (m *createSpusCollection) Name() string { return "2026_10_30_000020_create_spus_collection" }

func (m *createSpusCollection) Up(ctx context.Context, db *mongo.Database) error {
	if err := mongomigrate.EnsureCollection(ctx, db, "spus"); err != nil {
		return err
	}
	_, err := db.Collection("spus").Indexes().CreateOne(ctx,
		mongo.IndexModel{Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uq_spus_name")})
	return err
}

func (m *createSpusCollection) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection("spus").Drop(ctx)
}
