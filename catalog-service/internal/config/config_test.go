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
