// Package mongomigrate is the MongoDB half of the migration engine (docs/03 §4): same commands and batches as
// PostgreSQL, with a `migrations` collection as tracking table. MongoDB cannot run DDL atomically with the
// tracking write, so migrations must be idempotent (createIndexes is; use CreateCollection only when absent).
package mongomigrate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/console"
	"github.com/an-vt/ecom/platform-go/database/migration"
	"github.com/an-vt/ecom/platform-go/database/seeder"
)

// Migration is a MongoDB migration.
type Migration = migration.Migration[*mongo.Database]

// Migrations is the registry of the service; files call Register from init().
var Migrations = &migration.Set[*mongo.Database]{}

// Register adds a migration to the service registry.
func Register(m Migration) { Migrations.Register(m) }

// Seeder is a MongoDB seeder.
type Seeder = seeder.Seeder[*mongo.Database]

// Seeders is the seeder registry of the service.
var Seeders = &seeder.Set[*mongo.Database]{}

// RegisterSeeder adds a seeder to the service registry.
func RegisterSeeder(s Seeder) { Seeders.Register(s) }

const (
	trackingColl = "migrations"
	lockColl     = "migration_lock"
	lockTTL      = 2 * time.Minute
	lockPoll     = 100 * time.Millisecond
	lockWait     = 60 * time.Second
)

type record struct {
	Migration string    `bson:"migration"`
	Batch     int       `bson:"batch"`
	RanAt     time.Time `bson:"ran_at"`
}

// Store implements migration.Store[*mongo.Database].
type Store struct{ db *mongo.Database }

// NewStore creates the store.
func NewStore(db *mongo.Database) *Store { return &Store{db: db} }

// NewRunner builds a runner over the service registry (set nil = package registry).
func NewRunner(db *mongo.Database, set *migration.Set[*mongo.Database]) *migration.Runner[*mongo.Database] {
	if set == nil {
		set = Migrations
	}
	return migration.NewRunner[*mongo.Database](NewStore(db), set, nil)
}

// Lock takes a lease in the lock collection (it expires after lockTTL, so a crashed runner cannot block forever)
// and waits for another runner to finish. The lease carries an owner token, so a runner whose lease expired and
// was taken over never releases the new owner's lock.
func (s *Store) Lock(ctx context.Context) (func(), error) {
	coll := s.db.Collection(lockColl)
	owner := bson.NewObjectID().Hex()
	deadline := time.Now().Add(lockWait)
	for {
		now := time.Now().UTC()
		lease := bson.M{"expiresAt": now.Add(lockTTL), "owner": owner}

		// 1. take over an expired lease
		err := coll.FindOneAndUpdate(ctx, bson.M{"_id": "migrations", "expiresAt": bson.M{"$lt": now}}, bson.M{"$set": lease}).Err()
		if err == nil {
			return s.unlocker(owner), nil
		}
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, err
		}
		// 2. nobody holds it yet: create it (a concurrent runner gets a duplicate-key error and waits)
		_, err = coll.InsertOne(ctx, bson.M{"_id": "migrations", "expiresAt": lease["expiresAt"], "owner": owner})
		if err == nil {
			return s.unlocker(owner), nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for the migration lock")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

func (s *Store) unlocker(owner string) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.db.Collection(lockColl).DeleteOne(ctx, bson.M{"_id": "migrations", "owner": owner})
	}
}

// Prepare creates the unique index of the tracking collection.
func (s *Store) Prepare(ctx context.Context) error {
	_, err := s.db.Collection(trackingColl).Indexes().CreateOne(ctx,
		mongo.IndexModel{Keys: bson.D{{Key: "migration", Value: 1}}, Options: options.Index().SetUnique(true)})
	return err
}

// Applied lists the tracking collection.
func (s *Store) Applied(ctx context.Context) ([]migration.Record, error) {
	cur, err := s.db.Collection(trackingColl).Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "migration", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var rows []record
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]migration.Record, len(rows))
	for i, r := range rows {
		out[i] = migration.Record{Name: r.Migration, Batch: r.Batch, RanAt: r.RanAt}
	}
	return out, nil
}

// Apply runs Up, then records it.
func (s *Store) Apply(ctx context.Context, m Migration, batch int) error {
	if err := m.Up(ctx, s.db); err != nil {
		return err
	}
	_, err := s.db.Collection(trackingColl).InsertOne(ctx, record{Migration: m.Name(), Batch: batch, RanAt: time.Now().UTC()})
	return err
}

// Revert runs Down, then forgets it.
func (s *Store) Revert(ctx context.Context, m Migration) error {
	if err := m.Down(ctx, s.db); err != nil {
		return err
	}
	_, err := s.db.Collection(trackingColl).DeleteOne(ctx, bson.M{"migration": m.Name()})
	return err
}

// Fresh drops the whole database — local development only.
func (s *Store) Fresh(ctx context.Context) error {
	if err := s.db.Drop(ctx); err != nil {
		return fmt.Errorf("drop database: %w", err)
	}
	return nil
}

type seedRunner struct {
	db  *mongo.Database
	set *seeder.Set[*mongo.Database]
}

func (r seedRunner) Seed(ctx context.Context, class string) ([]string, error) {
	return r.set.Run(ctx, r.db, class)
}

// Console returns the migrator and seeder that the `console` binary hands to console.Run.
func Console(db *mongo.Database) (console.Migrator, console.Seeder) {
	return NewRunner(db, Migrations), seedRunner{db: db, set: Seeders}
}
