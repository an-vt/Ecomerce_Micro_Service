package redisx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/redisx"
)

func newRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return mr, c
}

func TestOpenPingsAndFailsFast(t *testing.T) {
	mr, _ := newRedis(t)
	c, err := redisx.Open(context.Background(), redisx.Config{Addr: mr.Addr()})
	require.NoError(t, err)
	require.NoError(t, c.Close())

	_, err = redisx.Open(context.Background(), redisx.Config{Addr: "127.0.0.1:1"})
	assert.Error(t, err)
}

func TestLimiterFixedWindow(t *testing.T) {
	mr, c := newRedis(t)
	l := redisx.NewLimiter(c, "rl:")
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		r, err := l.Allow(ctx, "ip:1.2.3.4", 3, time.Minute)
		require.NoError(t, err)
		assert.True(t, r.Allowed)
		assert.Equal(t, 3-i, r.Remaining)
	}
	r, err := l.Allow(ctx, "ip:1.2.3.4", 3, time.Minute)
	require.NoError(t, err)
	assert.False(t, r.Allowed)
	assert.Equal(t, 0, r.Remaining)
	assert.Greater(t, r.RetryAfter, 50*time.Second)

	other, err := l.Allow(ctx, "ip:9.9.9.9", 3, time.Minute)
	require.NoError(t, err)
	assert.True(t, other.Allowed, "keys are independent")

	mr.FastForward(time.Minute + time.Second)
	again, err := l.Allow(ctx, "ip:1.2.3.4", 3, time.Minute)
	require.NoError(t, err)
	assert.True(t, again.Allowed, "a new window starts after the TTL")
}

func TestLimiterAlwaysSetsAnExpiry(t *testing.T) {
	mr, c := newRedis(t)
	_, err := redisx.NewLimiter(c, "rl:").Allow(context.Background(), "k", 5, 30*time.Second)
	require.NoError(t, err)
	assert.Greater(t, mr.TTL("rl:k"), time.Duration(0))
}

func TestLimiterRepairsKeyWithoutExpiry(t *testing.T) {
	mr, c := newRedis(t)
	require.NoError(t, mr.Set("rl:stuck", "10")) // INCR on a key that has no TTL
	r, err := redisx.NewLimiter(c, "rl:").Allow(context.Background(), "stuck", 5, time.Minute)
	require.NoError(t, err)
	assert.False(t, r.Allowed)
	assert.Equal(t, time.Minute, r.RetryAfter)
}

func TestLimiterRedisDown(t *testing.T) {
	mr, c := newRedis(t)
	mr.Close()
	_, err := redisx.NewLimiter(c, "rl:").Allow(context.Background(), "k", 1, time.Minute)
	assert.Error(t, err)
}

func TestDenylist(t *testing.T) {
	mr, c := newRedis(t)
	d := redisx.NewDenylist(c, "auth:revoked:")
	ctx := context.Background()
	ok, err := d.IsRevoked(ctx, "sid-1")
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, d.Revoke(ctx, "sid-1", 15*time.Minute))
	ok, err = d.IsRevoked(ctx, "sid-1")
	require.NoError(t, err)
	assert.True(t, ok)

	mr.FastForward(16 * time.Minute)
	ok, _ = d.IsRevoked(ctx, "sid-1")
	assert.False(t, ok, "entries disappear when the token would have expired anyway")
}

type sku struct {
	ID    string `json:"id"`
	Price int64  `json:"price"`
}

func TestGetOrLoadCachesValuesAndMisses(t *testing.T) {
	_, c := newRedis(t)
	cache := redisx.NewCache(c, "sku:")
	ctx := context.Background()
	loads := 0
	load := func(found bool) redisx.Loader[sku] {
		return func(context.Context) (sku, bool, error) {
			loads++
			if !found {
				return sku{}, false, nil
			}
			return sku{ID: "s1", Price: 100}, true, nil
		}
	}

	v, found, hit, err := redisx.GetOrLoad(ctx, cache, "s1", time.Minute, 10*time.Second, load(true))
	require.NoError(t, err)
	assert.True(t, found)
	assert.False(t, hit)
	assert.Equal(t, sku{ID: "s1", Price: 100}, v)

	v, found, hit, err = redisx.GetOrLoad(ctx, cache, "s1", time.Minute, 10*time.Second, load(true))
	require.NoError(t, err)
	assert.True(t, found && hit)
	assert.Equal(t, int64(100), v.Price)
	assert.Equal(t, 1, loads, "second call served from cache")

	// unknown ids are cached too
	for i := 0; i < 3; i++ {
		_, found, _, err = redisx.GetOrLoad(ctx, cache, "ghost", time.Minute, 10*time.Second, load(false))
		require.NoError(t, err)
		assert.False(t, found)
	}
	assert.Equal(t, 2, loads, "the missing id hit the source once, then the null marker")
}

func TestNullEntryExpires(t *testing.T) {
	mr, c := newRedis(t)
	cache := redisx.NewCache(c, "sku:")
	ctx := context.Background()
	n := 0
	load := func(context.Context) (sku, bool, error) { n++; return sku{}, false, nil }
	_, _, _, _ = redisx.GetOrLoad(ctx, cache, "ghost", time.Minute, 10*time.Second, load)
	mr.FastForward(11 * time.Second)
	_, _, _, _ = redisx.GetOrLoad(ctx, cache, "ghost", time.Minute, 10*time.Second, load)
	assert.Equal(t, 2, n)
}

func TestLoaderErrorIsNotCached(t *testing.T) {
	_, c := newRedis(t)
	cache := redisx.NewCache(c, "p:")
	boom := errors.New("db down")
	_, _, _, err := redisx.GetOrLoad(context.Background(), cache, "k", time.Minute, time.Second,
		func(context.Context) (sku, bool, error) { return sku{}, false, boom })
	assert.ErrorIs(t, err, boom)
	v, found, _, err := redisx.GetOrLoad(context.Background(), cache, "k", time.Minute, time.Second,
		func(context.Context) (sku, bool, error) { return sku{ID: "ok"}, true, nil })
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "ok", v.ID)
}

func TestRedisOutageFallsBackToTheLoader(t *testing.T) {
	mr, c := newRedis(t)
	cache := redisx.NewCache(c, "p:")
	mr.Close()
	v, found, hit, err := redisx.GetOrLoad(context.Background(), cache, "k", time.Minute, time.Second,
		func(context.Context) (sku, bool, error) { return sku{ID: "from-db"}, true, nil })
	require.NoError(t, err, "a cache outage must not fail the request")
	assert.True(t, found)
	assert.False(t, hit)
	assert.Equal(t, "from-db", v.ID)
}

func TestCorruptEntryIsReloaded(t *testing.T) {
	mr, c := newRedis(t)
	require.NoError(t, mr.Set("p:k", "{not json"))
	v, found, hit, err := redisx.GetOrLoad(context.Background(), redisx.NewCache(c, "p:"), "k", time.Minute, time.Second,
		func(context.Context) (sku, bool, error) { return sku{ID: "fresh"}, true, nil })
	require.NoError(t, err)
	assert.True(t, found)
	assert.False(t, hit)
	assert.Equal(t, "fresh", v.ID)
}

func TestDelete(t *testing.T) {
	mr, c := newRedis(t)
	cache := redisx.NewCache(c, "p:")
	require.NoError(t, mr.Set("p:a", "1"))
	require.NoError(t, mr.Set("p:b", "1"))
	require.NoError(t, cache.Delete(context.Background(), "a", "b"))
	assert.False(t, mr.Exists("p:a"))
	assert.False(t, mr.Exists("p:b"))
}
