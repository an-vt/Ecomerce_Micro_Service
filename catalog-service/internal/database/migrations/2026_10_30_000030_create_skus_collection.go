package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&createSkusCollection{}) }

type createSkusCollection struct{}

func (m *createSkusCollection) Name() string { return "2026_10_30_000030_create_skus_collection" }

func (m *createSkusCollection) Up(ctx context.Context, db *mongo.Database) error {
	if err := mongomigrate.EnsureCollection(ctx, db, "skus"); err != nil {
		return err
	}
	_, err := db.Collection("skus").Indexes().CreateOne(ctx,
		mongo.IndexModel{Keys: bson.D{{Key: "spuId", Value: 1}, {Key: "sort", Value: 1}}, Options: options.Index().SetName("ix_skus_spu")})
	return err
}

func (m *createSkusCollection) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection("skus").Drop(ctx)
}
