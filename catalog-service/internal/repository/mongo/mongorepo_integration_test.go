//go:build integration

package mongorepo_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	_ "github.com/an-vt/ecom/catalog-service/internal/database/migrations"
	"github.com/an-vt/ecom/catalog-service/internal/domain"
	mongorepo "github.com/an-vt/ecom/catalog-service/internal/repository/mongo"
	"github.com/an-vt/ecom/catalog-service/internal/repotest"
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

// The MongoDB repositories must satisfy exactly the contract the in-memory fakes satisfy.
func TestMongoSatisfiesTheRepositoryContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		r := mongorepo.New(openMigrated(t))
		return repotest.Repos{Products: r.Products, Spus: r.Spus, Comments: r.Comments, Shops: r.Shops}
	})
}

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	runner := mongomigrate.NewRunner(store.DB(), nil)

	rows, err := runner.Status(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 6)
	for _, row := range rows {
		assert.True(t, row.Ran, row.Name)
	}
	names, err := store.DB().ListCollectionNames(ctx, bson.M{})
	require.NoError(t, err)
	for _, c := range []string{"products", "spus", "skus", "comments", "shops", "outbox_messages", "inbox_messages"} {
		assert.True(t, slices.Contains(names, c), c)
	}

	idx, err := store.DB().Collection("products").Indexes().ListSpecifications(ctx)
	require.NoError(t, err)
	var have []string
	for _, i := range idx {
		have = append(have, i.Name)
	}
	assert.Subset(t, have, []string{"uq_products_name", "ix_products_text", "ix_products_published", "ix_products_shop"})

	back, err := runner.Reset(ctx)
	require.NoError(t, err)
	assert.Len(t, back, 6)
	names, _ = store.DB().ListCollectionNames(ctx, bson.M{})
	assert.NotContains(t, names, "products")

	ran, err := runner.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, ran, 6, "up -> down -> up")
}

func TestNameAndSpuUniquenessIsEnforcedByTheDatabase(t *testing.T) {
	ctx := context.Background()
	r := mongorepo.New(openMigrated(t))
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, taken := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := domain.NewProduct(domain.DefaultTypes(), fmt.Sprintf("p%d", i), "shop", domain.ProductInput{Type: domain.TypeClothing, Name: "Same name",
				Price: 1, Attributes: map[string]any{"brand": "b", "size": "s", "material": "m"}}, repotestBase)
			require.NoError(t, err)
			err = r.Products.Create(ctx, p)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				created++
			} else {
				taken++
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, created)
	assert.Equal(t, 7, taken, "the unique index decides, not application code")
}

func TestConcurrentRepliesKeepTheTreeConsistent(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	r := mongorepo.New(store)
	root, err := domain.NewComment("root", "p1", "u", "root", "", repotestBase)
	require.NoError(t, err)
	require.NoError(t, r.Comments.AddRoot(ctx, root))

	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := domain.NewComment(fmt.Sprintf("r%02d", i), "p1", "u", "reply", "root", repotestBase)
			require.NoError(t, err)
			parent, err := r.Comments.FindByID(ctx, "root")
			require.NoError(t, err)
			require.NoError(t, r.Comments.AddReply(ctx, parent, c), "write conflicts are retried by the transaction")
		}()
	}
	wg.Wait()

	// 1 root + n replies: values are exactly 1..2(n+1), every reply lies inside the root
	cur, err := store.DB().Collection("comments").Find(ctx, bson.M{"productId": "p1"})
	require.NoError(t, err)
	var docs []struct {
		ID          string `bson:"_id"`
		Left, Right int64
	}
	require.NoError(t, cur.All(ctx, &docs))
	require.Len(t, docs, n+1)
	var vals []int64
	var rootDoc struct{ Left, Right int64 }
	for _, d := range docs {
		vals = append(vals, d.Left, d.Right)
		if d.ID == "root" {
			rootDoc.Left, rootDoc.Right = d.Left, d.Right
		}
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	for i, v := range vals {
		require.Equal(t, int64(i+1), v, "no gaps and no duplicates after %d concurrent replies: %v", n, vals)
	}
	for _, d := range docs {
		if d.ID != "root" {
			assert.True(t, rootDoc.Left < d.Left && d.Right < rootDoc.Right, "%s is inside the root", d.ID)
		}
	}
	kids, err := r.Comments.List(ctx, "p1", "root", 100, 0)
	require.NoError(t, err)
	assert.Len(t, kids, n)
}

func TestAtomicProductAndEventWrites(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	r := mongorepo.New(store)
	tx := mongox.NewTxManager(store)

	p, _ := domain.NewProduct(domain.DefaultTypes(), "p1", "shop", domain.ProductInput{Type: domain.TypeClothing, Name: "Tx product", Price: 1,
		Attributes: map[string]any{"brand": "b", "size": "s", "material": "m"}}, repotestBase)
	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := r.Products.Create(ctx, p); err != nil {
			return err
		}
		return fmt.Errorf("event could not be written")
	})
	require.Error(t, err)
	_, err = r.Products.FindByID(ctx, "p1")
	assert.Equal(t, domain.CodeProductNotFound, errsCode(err), "the product insert rolled back with the failed transaction")
}
