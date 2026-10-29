//go:build integration

package mongomigrate_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/an-vt/ecom/platform-go/database/migration"
	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/database/seeder"
	"github.com/an-vt/ecom/platform-go/mongox"
	"github.com/an-vt/ecom/platform-go/testkit"
)

type collMigration struct{ name, coll string }

func (m collMigration) Name() string { return m.name }
func (m collMigration) Up(ctx context.Context, db *mongo.Database) error {
	if err := db.CreateCollection(ctx, m.coll); err != nil {
		return err
	}
	_, err := db.Collection(m.coll).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "createdAt", Value: 1}}})
	return err
}
func (m collMigration) Down(ctx context.Context, db *mongo.Database) error {
	return db.Collection(m.coll).Drop(ctx)
}

var (
	products = collMigration{"2026_10_30_000010_create_products_collection", "products"}
	carts    = collMigration{"2026_10_30_000020_create_carts_collection", "carts"}
)

func setup(t *testing.T, ms ...mongomigrate.Migration) (*mongox.Store, *migration.Runner[*mongo.Database]) {
	t.Helper()
	uri, name := testkit.MongoURI(t)
	s, err := mongox.Open(context.Background(), mongox.Config{URI: uri, Database: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.DB().Drop(context.Background()); _ = s.Close(context.Background()) })
	set := &migration.Set[*mongo.Database]{}
	for _, m := range ms {
		set.Register(m)
	}
	return s, mongomigrate.NewRunner(s.DB(), set)
}

func collections(t *testing.T, s *mongox.Store) []string {
	t.Helper()
	names, err := s.DB().ListCollectionNames(context.Background(), bson.M{})
	require.NoError(t, err)
	return names
}

func TestMigrateRollbackMigrate(t *testing.T) {
	ctx := context.Background()
	s, r := setup(t, products, carts)

	ran, err := r.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, []string{products.name, carts.name}, ran)
	assert.True(t, slices.Contains(collections(t, s), "products"))
	assert.True(t, slices.Contains(collections(t, s), "carts"))

	rows, err := r.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, []migration.StatusRow{{Name: products.name, Ran: true, Batch: 1}, {Name: carts.name, Ran: true, Batch: 1}}, rows)

	back, err := r.Rollback(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{carts.name, products.name}, back)
	assert.False(t, slices.Contains(collections(t, s), "products"))

	ran, err = r.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, ran, 2, "up -> down -> up")
}

func TestTwoRunnersApplyEachMigrationOnce(t *testing.T) {
	ctx := context.Background()
	s, r1 := setup(t, products, carts)
	set := &migration.Set[*mongo.Database]{}
	set.Register(products)
	set.Register(carts)
	r2 := mongomigrate.NewRunner(s.DB(), set)

	var wg sync.WaitGroup
	results := make([][]string, 2)
	errs := make([]error, 2)
	for i, r := range []*migration.Runner[*mongo.Database]{r1, r2} {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], errs[i] = r.Migrate(ctx, false) }()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, 2, len(results[0])+len(results[1]), "each migration ran exactly once across both runners")
}

func TestExpiredLeaseIsTakenOverAndOwnerSafe(t *testing.T) {
	ctx := context.Background()
	s, r := setup(t, products)
	// a crashed runner left an expired lease behind
	_, err := s.DB().Collection("migration_lock").InsertOne(ctx, bson.M{"_id": "migrations", "expiresAt": time.Now().Add(-time.Minute), "owner": "dead"})
	require.NoError(t, err)

	ran, err := r.Migrate(ctx, false)
	require.NoError(t, err, "an expired lease must not block migrations forever")
	assert.Len(t, ran, 1)
	n, err := s.DB().Collection("migration_lock").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "the lease is released")
}

func TestFreshDropsTheDatabase(t *testing.T) {
	ctx := context.Background()
	s, r := setup(t, products)
	_, err := r.Migrate(ctx, false)
	require.NoError(t, err)
	_, err = s.DB().Collection("leftover").InsertOne(ctx, bson.M{"x": 1})
	require.NoError(t, err)

	ran, err := r.Fresh(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{products.name}, ran)
	assert.False(t, slices.Contains(collections(t, s), "leftover"))
	assert.True(t, slices.Contains(collections(t, s), "products"))
}

type markSeeder struct{}

func (markSeeder) Name() string { return "MarkSeeder" }
func (markSeeder) Run(ctx context.Context, db *mongo.Database, _ *seeder.Set[*mongo.Database]) error {
	_, err := db.Collection("seeded").UpdateOne(ctx, bson.M{"_id": "x"}, bson.M{"$set": bson.M{"ok": true}}, optionsUpsert())
	return err
}

func TestRegistriesAndConsole(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	mongomigrate.Register(products)
	mongomigrate.RegisterSeeder(markSeeder{})
	m, sd := mongomigrate.Console(s.DB())
	_, err := m.Migrate(ctx, false)
	require.NoError(t, err)
	started, err := sd.Seed(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"MarkSeeder"}, started)
	_, err = sd.Seed(ctx, "")
	require.NoError(t, err, "seeders are idempotent")
	n, err := s.DB().Collection("seeded").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}
