// Package redisx wraps go-redis for what the services need: a traced client, an atomic fixed-window rate limiter,
// the revoked-session denylist, and a cache-aside helper that also caches "not found" (docs/04, docs/07).
package redisx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
)

// Config for Open.
type Config struct {
	Addr     string
	Password string
	DB       int
}

// Open connects with tracing and short command timeouts (cache failures must never stall a request) and pings.
func Open(ctx context.Context, c Config) (*redis.Client, error) {
	cl := redis.NewClient(&redis.Options{
		Addr: c.Addr, Password: c.Password, DB: c.DB,
		DialTimeout: 2 * time.Second, ReadTimeout: 500 * time.Millisecond, WriteTimeout: 500 * time.Millisecond,
	})
	if err := redisotel.InstrumentTracing(cl); err != nil {
		_ = cl.Close()
		return nil, fmt.Errorf("redis tracing: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := cl.Ping(pctx).Err(); err != nil {
		_ = cl.Close()
		return nil, fmt.Errorf("redis: %w", err)
	}
	return cl, nil
}

// ---- rate limiter ---------------------------------------------------------------------------------------------

// Limiter is a fixed-window counter. The increment and the expiry are one Lua script, so a crash between them can
// never leave a key without TTL.
type Limiter struct {
	c      redis.Scripter
	prefix string
}

// NewLimiter creates a limiter whose keys start with prefix.
func NewLimiter(c redis.Scripter, prefix string) *Limiter { return &Limiter{c: c, prefix: prefix} }

// Result of Allow.
type Result struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration // when Allowed is false: time until the window resets
}

var allowScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return {n, redis.call('PTTL', KEYS[1])}
`)

// Allow counts one request for key and reports whether it is within limit per window.
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (Result, error) {
	res, err := allowScript.Run(ctx, l.c, []string{l.prefix + key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return Result{}, err
	}
	n, ttl := int(res[0]), time.Duration(res[1])*time.Millisecond
	if ttl < 0 { // key without expiry (should not happen): repair it
		ttl = window
	}
	if n > limit {
		return Result{Allowed: false, Remaining: 0, RetryAfter: ttl}, nil
	}
	return Result{Allowed: true, Remaining: limit - n}, nil
}

// ---- revoked sessions -----------------------------------------------------------------------------------------

// Denylist holds revoked session ids until their access tokens would have expired (docs/07 §2.2).
type Denylist struct {
	c      redis.Cmdable
	prefix string
}

// NewDenylist creates a denylist; prefix is typically "auth:revoked:".
func NewDenylist(c redis.Cmdable, prefix string) *Denylist { return &Denylist{c: c, prefix: prefix} }

// Revoke marks sid revoked for ttl.
func (d *Denylist) Revoke(ctx context.Context, sid string, ttl time.Duration) error {
	return d.c.Set(ctx, d.prefix+sid, "1", ttl).Err()
}

// IsRevoked reports whether sid is on the list.
func (d *Denylist) IsRevoked(ctx context.Context, sid string) (bool, error) {
	n, err := d.c.Exists(ctx, d.prefix+sid).Result()
	return n > 0, err
}

// ---- cache-aside with null caching ---------------------------------------------------------------------------

const nullMarker = "\x00null"

// Cache stores JSON values with a TTL.
type Cache struct {
	c      redis.Cmdable
	prefix string
}

// NewCache creates a cache whose keys start with prefix.
func NewCache(c redis.Cmdable, prefix string) *Cache { return &Cache{c: c, prefix: prefix} }

// Loader fetches the value from the source of truth; found=false means it does not exist.
type Loader[T any] func(ctx context.Context) (value T, found bool, err error)

// GetOrLoad returns the cached value or loads and caches it. A missing value is cached for nullTTL as well, so
// repeated lookups of an unknown id do not hit the database (cache-penetration guard, ported from the monolith).
// Redis problems are never fatal: the loader is used instead and the value is simply not cached.
// The result's hit is true when it came from the cache.
func GetOrLoad[T any](ctx context.Context, c *Cache, key string, ttl, nullTTL time.Duration, load Loader[T]) (value T, found, hit bool, err error) {
	var zero T
	k := c.prefix + key
	raw, gerr := c.c.Get(ctx, k).Result()
	switch {
	case gerr == nil && raw == nullMarker:
		return zero, false, true, nil
	case gerr == nil:
		if uerr := json.Unmarshal([]byte(raw), &value); uerr == nil {
			return value, true, true, nil
		}
		// corrupt entry: fall through and reload
	case !errors.Is(gerr, redis.Nil):
		// Redis unavailable: use the source of truth, skip caching
		v, ok, lerr := load(ctx)
		return v, ok, false, lerr
	}

	v, ok, lerr := load(ctx)
	if lerr != nil {
		return zero, false, false, lerr
	}
	if !ok {
		_ = c.c.Set(ctx, k, nullMarker, nullTTL).Err()
		return zero, false, false, nil
	}
	if b, merr := json.Marshal(v); merr == nil {
		_ = c.c.Set(ctx, k, b, ttl).Err()
	}
	return v, true, false, nil
}

// Delete removes keys (invalidation after a write).
func (c *Cache) Delete(ctx context.Context, keys ...string) error {
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = c.prefix + k
	}
	return c.c.Del(ctx, full...).Err()
}
