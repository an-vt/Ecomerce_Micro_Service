package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&createProductsCollection{}) }

type createProductsCollection struct{}

func (m *createProductsCollection) Name() string {
	return "2026_10_30_000010_create_products_collection"
}

func (m *createProductsCollection) Up(ctx context.Context, db *mongo.Database) error {
	if err := mongomigrate.EnsureCollection(ctx, db, "products"); err != nil {
		return err
	}
	_, err := db.Collection("products").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uq_products_name")},
		// one text index per collection: product search (name weighs more than the description)
		{Keys: bson.D{{Key: "name", Value: "text"}, {Key: "description", Value: "text"}},
			Options: options.Index().SetName("ix_products_text").SetWeights(bson.D{{Key: "name", Value: 3}, {Key: "description", Value: 1}})},
		{Keys: bson.D{{Key: "isPublished", Value: 1}, {Key: "createdAt", Value: -1}}, Options: options.Index().SetName("ix_products_published")},
		{Keys: bson.D{{Key: "shopId", Value: 1}, {Key: "isPublished", Value: 1}, {Key: "createdAt", Value: -1}}, Options: options.Index().SetName("ix_products_shop")},
	})
	return err
}

func (m *createProductsCollection) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection("products").Drop(ctx)
}
