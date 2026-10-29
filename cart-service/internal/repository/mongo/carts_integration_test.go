//go:build integration

package mongorepo_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	_ "github.com/an-vt/ecom/cart-service/internal/database/migrations"
	"github.com/an-vt/ecom/cart-service/internal/domain"
	mongorepo "github.com/an-vt/ecom/cart-service/internal/repository/mongo"
	"github.com/an-vt/ecom/cart-service/internal/repotest"
	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/mongox"
	"github.com/an-vt/ecom/platform-go/testkit"
)

func openMigrated(t *testing.T) *mongox.Store {
	t.Helper()
	ctx := context.Background()
	uri, name := testkit.MongoURI(t)
	store, err := mongox.Open(ctx, mongox.Config{URI: uri, Database: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DB().Drop(ctx); _ = store.Close(ctx) })
	_, err = mongomigrate.NewRunner(store.DB(), nil).Migrate(ctx, false)
	require.NoError(t, err)
	return store
}

func TestMongoSatisfiesTheRepositoryContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) domain.CartRepository { return mongorepo.New(openMigrated(t)) })
}

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	runner := mongomigrate.NewRunner(store.DB(), nil)
	rows, err := runner.Status(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
	names, _ := store.DB().ListCollectionNames(ctx, bson.M{})
	assert.True(t, slices.Contains(names, "carts"))
	back, err := runner.Reset(ctx)
	require.NoError(t, err)
	assert.Len(t, back, 2)
	ran, err := runner.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, ran, 2, "up -> down -> up")
}

// Many requests of one buyer racing on the same cart: with the retry loop every one of them lands, nothing is lost.
func TestConcurrentAddsFromManyDevicesAreAllKept(t *testing.T) {
	ctx := context.Background()
	repo := mongorepo.New(openMigrated(t))
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for attempt := 0; attempt < 50; attempt++ { // what the use case's mutate loop does
				c, err := repo.FindByUser(ctx, "buyer")
				require.NoError(t, err)
				if c == nil {
					c, _ = domain.NewCart("00000000-0000-4000-8000-000000000001", "buyer", now)
				}
				require.NoError(t, c.Add(domain.Item{ProductID: "p1", ShopID: "s", Name: "n", Price: 10, Quantity: 1}, now))
				if err := repo.Save(ctx, c); err == nil {
					return
				}
			}
			t.Error("gave up")
		}()
	}
	wg.Wait()
	c, err := repo.FindByUser(ctx, "buyer")
	require.NoError(t, err)
	assert.Equal(t, int64(n), c.QuantityOf("p1"), "every increment survived: no lost update")
	assert.Equal(t, int64(n), c.Version())
}
