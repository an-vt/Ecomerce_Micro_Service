package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/catalog-service/internal/cache"
	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/redisx"
)

func newCache(t *testing.T) (*miniredis.Miniredis, *cache.Sku) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), DialerRetries: 1, MaxRetries: 1, DialTimeout: 200 * time.Millisecond, ReadTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, cache.NewSku(redisx.NewCache(rdb, "sku:"), 30*time.Second)
}

var sku = &domain.Sku{ID: "spu1.1", SpuID: "spu1", TierIdx: []int{0, 1}, IsDefault: true, Slug: "t-shirt", Sort: 2, Price: 100, Stock: 7}

func TestLookupCachesTheSkuAndRoundTripsItExactly(t *testing.T) {
	mr, c := newCache(t)
	loads := 0
	load := func(context.Context) (*domain.Sku, bool, error) { loads++; return sku, true, nil }

	got, found, err := c.Lookup(context.Background(), "spu1.1", load)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, sku, got)

	got, found, err = c.Lookup(context.Background(), "spu1.1", load)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, sku, got, "the cached copy equals the original")
	assert.Equal(t, 1, loads)
	assert.True(t, mr.Exists("sku:spu1.1"))
	assert.Equal(t, 30*time.Second, mr.TTL("sku:spu1.1"))
}

func TestLookupCachesMisses(t *testing.T) {
	_, c := newCache(t)
	loads := 0
	load := func(context.Context) (*domain.Sku, bool, error) { loads++; return nil, false, nil }
	for i := 0; i < 4; i++ {
		got, found, err := c.Lookup(context.Background(), "ghost", load)
		require.NoError(t, err)
		assert.False(t, found)
		assert.Nil(t, got)
	}
	assert.Equal(t, 1, loads, "probing an unknown id hits the database once")
}

func TestLookupErrorsAreNotCachedAndRedisOutageFallsBack(t *testing.T) {
	mr, c := newCache(t)
	boom := errors.New("mongo down")
	_, _, err := c.Lookup(context.Background(), "x", func(context.Context) (*domain.Sku, bool, error) { return nil, false, boom })
	assert.ErrorIs(t, err, boom)

	got, found, err := c.Lookup(context.Background(), "x", func(context.Context) (*domain.Sku, bool, error) { return sku, true, nil })
	require.NoError(t, err, "the failure was not cached")
	assert.True(t, found)
	assert.Equal(t, sku, got)

	mr.Close()
	got, found, err = c.Lookup(context.Background(), "y", func(context.Context) (*domain.Sku, bool, error) { return sku, true, nil })
	require.NoError(t, err, "a cache outage must not fail the request")
	assert.True(t, found)
	assert.Equal(t, sku, got)
}
