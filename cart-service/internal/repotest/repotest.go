// Package repotest is the repository contract suite: the same tests run against the in-memory fake and against
// MongoDB, so the fake cannot promise anything the real store does not do.
package repotest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// New returns a fresh, empty repository for one test.
type New func(t *testing.T) domain.CartRepository

var ctx = context.Background()
var base = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func item(product string, q int64) domain.Item {
	return domain.Item{ProductID: product, ShopID: "shop-1", Name: "Item " + product, Price: 1000, Quantity: q}
}

// Run executes the whole suite.
func Run(t *testing.T, newRepo New) {
	t.Run("RoundTrip", func(t *testing.T) { roundTrip(t, newRepo(t)) })
	t.Run("OptimisticConcurrency", func(t *testing.T) { concurrency(t, newRepo(t)) })
	t.Run("OneCartPerUser", func(t *testing.T) { onePerUser(t, newRepo(t)) })
}

func roundTrip(t *testing.T, r domain.CartRepository) {
	got, err := r.FindByUser(ctx, "u1")
	require.NoError(t, err)
	assert.Nil(t, got, "no cart yet: nil, no error")

	c, _ := domain.NewCart("00000000-0000-4000-8000-000000000001", "u1", base)
	require.NoError(t, c.Add(item("p1", 2), base))
	require.NoError(t, c.Add(item("p2", 1), base))
	require.NoError(t, r.Save(ctx, c))
	assert.Equal(t, int64(1), c.Version(), "a save bumps the version")

	got, err = r.FindByUser(ctx, "u1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, c.Snapshot(), got.Snapshot(), "round trip keeps every field, in line order")
	assert.Equal(t, int64(2), got.QuantityOf("p1"))

	got.Remove("p1", base.Add(time.Minute))
	require.NoError(t, r.Save(ctx, got))
	again, _ := r.FindByUser(ctx, "u1")
	assert.Equal(t, int64(2), again.Version())
	assert.Equal(t, int64(0), again.QuantityOf("p1"))
	assert.Equal(t, 1, again.Lines())
}

func concurrency(t *testing.T, r domain.CartRepository) {
	c, _ := domain.NewCart("00000000-0000-4000-8000-000000000002", "u1", base)
	require.NoError(t, c.Add(item("p1", 1), base))
	require.NoError(t, r.Save(ctx, c))

	// two devices load the same version
	a, _ := r.FindByUser(ctx, "u1")
	b, _ := r.FindByUser(ctx, "u1")
	require.NoError(t, a.Add(item("p2", 1), base))
	require.NoError(t, b.Add(item("p3", 1), base))
	require.NoError(t, r.Save(ctx, a))
	err := r.Save(ctx, b)
	require.Error(t, err)
	assert.Equal(t, domain.CodeCartConflict, errs.CodeOf(err), "the second writer must not overwrite the first")
	assert.Equal(t, errs.KindAborted, errs.KindOf(err))

	final, _ := r.FindByUser(ctx, "u1")
	assert.Equal(t, int64(1), final.QuantityOf("p2"))
	assert.Equal(t, int64(0), final.QuantityOf("p3"), "the losing change is not half-applied")
}

func onePerUser(t *testing.T, r domain.CartRepository) {
	const racers = 6
	var created, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := domain.NewCart("00000000-0000-4000-8000-0000000001"+string(rune('0'+i))+"0", "same-user", base)
			_ = c.Add(item("p1", 1), base)
			switch err := r.Save(ctx, c); {
			case err == nil:
				created.Add(1)
			case errs.CodeOf(err) == domain.CodeCartConflict:
				conflicts.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), created.Load(), "a buyer never ends up with two carts")
	assert.Equal(t, int32(racers-1), conflicts.Load())
}
