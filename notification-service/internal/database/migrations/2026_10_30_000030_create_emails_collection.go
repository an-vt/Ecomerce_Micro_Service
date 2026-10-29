package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&createEmailsCollection{}) }

type createEmailsCollection struct{}

func (m *createEmailsCollection) Name() string { return "2026_10_30_000030_create_emails_collection" }

func (m *createEmailsCollection) Up(ctx context.Context, db *mongo.Database) error {
	if err := mongomigrate.EnsureCollection(ctx, db, "emails"); err != nil {
		return err
	}
	// one record per causing message and template: what lets a redelivered message find out it was already sent
	_, err := db.Collection("emails").Indexes().CreateOne(ctx,
		mongo.IndexModel{Keys: bson.D{{Key: "dedupeKey", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uq_emails_dedupe")})
	return err
}

func (m *createEmailsCollection) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection("emails").Drop(ctx)
}
