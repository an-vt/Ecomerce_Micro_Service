package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&createCartsCollection{}) }

type createCartsCollection struct{}

func (m *createCartsCollection) Name() string { return "2026_10_30_000010_create_carts_collection" }

func (m *createCartsCollection) Up(ctx context.Context, db *mongo.Database) error {
	if err := mongomigrate.EnsureCollection(ctx, db, "carts"); err != nil {
		return err
	}
	// one cart per buyer: this index is what makes two concurrent first saves safe
	_, err := db.Collection("carts").Indexes().CreateOne(ctx,
		mongo.IndexModel{Keys: bson.D{{Key: "userId", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uq_carts_user")})
	return err
}

func (m *createCartsCollection) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection("carts").Drop(ctx)
}
