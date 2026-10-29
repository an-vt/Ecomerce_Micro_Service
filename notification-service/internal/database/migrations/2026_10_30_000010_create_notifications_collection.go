package migrations

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&createNotificationsCollection{}) }

type createNotificationsCollection struct{}

func (m *createNotificationsCollection) Name() string {
	return "2026_10_30_000010_create_notifications_collection"
}

func (m *createNotificationsCollection) Up(ctx context.Context, db *mongo.Database) error {
	if err := mongomigrate.EnsureCollection(ctx, db, "notifications"); err != nil {
		return err
	}
	_, err := db.Collection("notifications").Indexes().CreateMany(ctx, []mongo.IndexModel{
		// listing: a receiver's notifications, newest first
		{Keys: bson.D{{Key: "receiverId", Value: 1}, {Key: "createdAt", Value: -1}}, Options: options.Index().SetName("ix_notifications_receiver_created")},
		// one notification per causing message and receiver: a redelivered message cannot create a second
		{Keys: bson.D{{Key: "dedupeKey", Value: 1}}, Options: options.Index().SetUnique(true).SetName("uq_notifications_dedupe")},
	})
	return err
}

func (m *createNotificationsCollection) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection("notifications").Drop(ctx)
}
