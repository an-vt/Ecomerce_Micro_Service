package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/catalog-service/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
)

func loader(env map[string]string) *platform.Loader {
	return platform.NewLoaderFrom(platform.Map(env), func(string) ([]byte, error) { return nil, assert.AnError })
}

func TestLoad(t *testing.T) {
	c, err := config.Load(loader(map[string]string{"MONGO_URI": "mongodb://x"}))
	require.NoError(t, err)
	assert.Equal(t, 8102, c.HTTPPort)
	assert.Equal(t, 9102, c.GRPCPort)
	assert.Equal(t, "catalog_db", c.MongoDB)
	assert.Equal(t, 30*time.Second, c.SkuCacheTTL)
	assert.True(t, c.AutoMigrate)

	c, err = config.Load(loader(map[string]string{"MONGO_URI": "m", "SKU_CACHE_TTL_SEC": "5", "AUTO_MIGRATE": "false"}))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, c.SkuCacheTTL)
	assert.False(t, c.AutoMigrate)

	_, err = config.Load(loader(nil))
	assert.ErrorContains(t, err, "MONGO_URI")
}

func TestMediaIsOffByDefault(t *testing.T) {
	c, err := config.Load(loader(map[string]string{"MONGO_URI": "mongodb://x"}))
	require.NoError(t, err)
	assert.False(t, c.MediaEnabled())
	assert.Equal(t, 5*time.Minute, c.MediaURLTTL)
	assert.EqualValues(t, 5<<20, c.MediaMaxBytes)
	assert.True(t, c.MediaPathStyle)
	assert.Equal(t, "us-east-1", c.MediaRegion)
}

func TestMediaConfiguredInFull(t *testing.T) {
	c, err := config.Load(loader(map[string]string{"MONGO_URI": "m", "MEDIA_ENDPOINT": "http://localhost:8000", "MEDIA_BUCKET": "ecom-media", "MEDIA_ACCESS_KEY": "ak",
		"MEDIA_SECRET_KEY": "sk", "MEDIA_PUBLIC_BASE_URL": "https://cdn.example.com", "MEDIA_PATH_STYLE": "false", "MEDIA_URL_TTL_SEC": "120", "MEDIA_MAX_BYTES": "1048576", "MEDIA_REGION": "eu-west-1"}))
	require.NoError(t, err)
	assert.True(t, c.MediaEnabled())
	assert.Equal(t, "ecom-media", c.MediaBucket)
	assert.Equal(t, "sk", c.MediaSecretKey)
	assert.False(t, c.MediaPathStyle)
	assert.Equal(t, 2*time.Minute, c.MediaURLTTL)
	assert.EqualValues(t, 1<<20, c.MediaMaxBytes)
	assert.Equal(t, "eu-west-1", c.MediaRegion)
}

// A half-configured store would fail on the first upload, in production; it fails at start-up instead.
func TestHalfConfiguredMediaFailsAtStartup(t *testing.T) {
	full := map[string]string{"MONGO_URI": "m", "MEDIA_ENDPOINT": "http://localhost:8000", "MEDIA_BUCKET": "b", "MEDIA_ACCESS_KEY": "ak", "MEDIA_SECRET_KEY": "sk"}
	for _, missing := range []string{"MEDIA_ENDPOINT", "MEDIA_BUCKET", "MEDIA_ACCESS_KEY", "MEDIA_SECRET_KEY"} {
		env := map[string]string{}
		for k, v := range full {
			if k != missing {
				env[k] = v
			}
		}
		_, err := config.Load(loader(env))
		require.Error(t, err, missing)
		assert.ErrorContains(t, err, missing)
	}
	for name, extra := range map[string]map[string]string{
		"ttl too short": {"MEDIA_URL_TTL_SEC": "5"}, "ttl too long": {"MEDIA_URL_TTL_SEC": "7200"}, "no size limit": {"MEDIA_MAX_BYTES": "0"},
	} {
		env := map[string]string{}
		for k, v := range full {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		_, err := config.Load(loader(env))
		assert.Error(t, err, name)
	}
}
