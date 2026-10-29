package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&createCommentsCollection{}) }

type createCommentsCollection struct{}

func (m *createCommentsCollection) Name() string {
	return "2026_10_30_000040_create_comments_collection"
}

func (m *createCommentsCollection) Up(ctx context.Context, db *mongo.Database) error {
	if err := mongomigrate.EnsureCollection(ctx, db, "comments"); err != nil {
		return err
	}
	_, err := db.Collection("comments").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "productId", Value: 1}, {Key: "left", Value: 1}}, Options: options.Index().SetName("ix_comments_left")},
		{Keys: bson.D{{Key: "productId", Value: 1}, {Key: "right", Value: 1}}, Options: options.Index().SetName("ix_comments_right")},
		{Keys: bson.D{{Key: "productId", Value: 1}, {Key: "parentId", Value: 1}, {Key: "left", Value: 1}}, Options: options.Index().SetName("ix_comments_children")},
	})
	return err
}

func (m *createCommentsCollection) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection("comments").Drop(ctx)
}
